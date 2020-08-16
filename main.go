package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"github.com/rcrowley/go-metrics"
)

type agent struct {
	id     string
	apiKey string
}

type policyCounter struct {
	sync.Mutex
	revisionsByAgent map[string]int
	revisionsSummary map[int]int
}

func (c *policyCounter) Set(agentName string, revision int) {
	c.Lock()

	prevRev, ok := c.revisionsByAgent[agentName]
	if ok {
		c.revisionsSummary[prevRev]--
	}
	c.revisionsByAgent[agentName] = revision
	c.revisionsSummary[revision]++
	c.Unlock()
}

func (c *policyCounter) Summary() map[int]int {
	newSummary := map[int]int{}
	c.Lock()
	for k, v := range c.revisionsSummary {
		newSummary[k] = v
	}
	c.Unlock()
	return newSummary
}

var policies = policyCounter{
	revisionsByAgent: map[string]int{},
	revisionsSummary: map[int]int{},
}

type testState string

const (
	// testStateEnrolling      testState = "enrolling"
	testStatePolicyChanging testState = "policy-changing"
	testStateIdle           testState = "idle"
)

type agentEvent string

const (
	// agentEventEnrolled      agentEvent = "enrolled"
	agentEventPolicyChanged agentEvent = "policy-updated"
)

type testStateMachine struct {
	state                  testState
	agentPolicyUpdateCount int32
	totalAgents            int
	stateChanged           chan testState
}

func (s *testStateMachine) PolicyUpdated() {
	newVal := atomic.AddInt32(&s.agentPolicyUpdateCount, 1)
	if newVal == int32(s.totalAgents) {
		s.state = testStateIdle
		s.stateChanged <- testStateIdle
	}
}

func (s *testStateMachine) Changed() <-chan testState {
	return s.stateChanged
}

func (s *testStateMachine) Reset() {
	s.agentPolicyUpdateCount = 0
	s.state = testStatePolicyChanging
}

func newTestStateMachine(agentCount int) testStateMachine {
	return testStateMachine{
		state:                  testStatePolicyChanging,
		agentPolicyUpdateCount: 0,
		stateChanged:           make(chan testState, 1),
		totalAgents:            agentCount,
	}
}

var stateMachine testStateMachine

var errUnexpectedStatusCode = errors.New("unexpected status code")
var client *http.Client

func init() {
	transCfg := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // ignore expired SSL certificates
	}

	client = &http.Client{Transport: transCfg}
}

func request(req *http.Request, reqName string) (*http.Response, error) {
	t := metrics.GetOrRegisterTimer("requests."+reqName+".latency", nil)
	var resp *http.Response
	var err error
	c := metrics.GetOrRegisterCounter("requests."+reqName+".concurrent_count", nil)
	c.Inc(1)
	t.Time(func() {
		resp, err = client.Do(req)
	})
	c.Dec(1)
	return resp, err
}

func enroll(ctx context.Context, agentName string, host, token string) (agent, error) {
	reqBody := fmt.Sprintf(`{"type": "PERMANENT", "metadata": {"user_provided": {}, "local": {"elastic": {"agent": {"version": "7.9.0"}}, "host": {"hostname": "%s"}}}}`, agentName)

	req, err := http.NewRequestWithContext(ctx, "POST", host+"/api/ingest_manager/fleet/agents/enroll", bytes.NewBufferString(reqBody))
	req.Header.Add("Content-type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("ApiKey %s", token))
	req.Header.Add("kbn-xsrf", "false")

	resp, err := request(req, "enroll")
	if err != nil {
		return agent{}, errors.Wrap(err, "enrolling")
	}
	if resp.StatusCode == 429 {
		return agent{}, errors.Wrap(errUnexpectedStatusCode, fmt.Sprintf("code: %d", resp.StatusCode))
	}

	if resp.StatusCode != 200 {
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return agent{}, errors.Wrap(err, "err while reading non 200 response body")
		}
		return agent{}, errors.Wrap(errUnexpectedStatusCode, fmt.Sprintf("code: %d, body: %s", resp.StatusCode, body))
	}

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return agent{}, errors.Wrap(err, "reading response")
	}

	var out map[string]interface{}
	err = json.Unmarshal(b, &out)
	if err != nil {
		return agent{}, errors.Wrap(err, "decoding response")
	}
	return agent{
		id:     out["item"].(map[string]interface{})["id"].(string),
		apiKey: out["item"].(map[string]interface{})["access_api_key"].(string),
	}, nil
}

func checkin(ctx context.Context, agentName string, agent agent, host string, first bool) error {
	reqBody := `{}`
	if first {
		reqBody = fmt.Sprintf(`{
	"events": [{
			"type": "STATE",
			"subtype": "RUNNING",
			"message": "state changed from STOPPED to RUNNING",
			"timestamp": "%s",
			"payload": {
				"random": "data",
				"state": "RUNNING",
				"previous_state": "STOPPED"
			},
			"agent_id": "%s"
		}]
	}`, time.Now().Format(time.RFC3339), agent.id)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/api/ingest_manager/fleet/agents/%s/checkin", host, agent.id), bytes.NewBufferString(reqBody))
	req.Header.Add("Content-type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("ApiKey %s", agent.apiKey))
	req.Header.Add("kbn-xsrf", "false")

	var metricName string
	if first {
		metricName = "first-checkin"
	} else {
		metricName = "checkin"
	}
	checkinResp, err := request(req, metricName)
	if err != nil {
		return errors.Wrap(err, metricName)
	}

	if checkinResp.StatusCode != 200 {
		body, err := ioutil.ReadAll(checkinResp.Body)
		if err != nil {
			return errors.Wrap(err, "err while reading non 200 response body")
		}

		return fmt.Errorf("unexpected: %s", body)
	}

	body, err := ioutil.ReadAll(checkinResp.Body)
	if err != nil {
		return err
	}
	var out map[string]interface{}
	err = json.Unmarshal(body, &out)
	if err != nil {
		return errors.Wrap(err, "decoding response")
	}

	actions := out["actions"].([]interface{})
	acks := []map[string]interface{}{}
	for _, a := range actions {
		b := a.(map[string]interface{})
		acks = append(acks, map[string]interface{}{
			"type":      "ACTION_RESULT",
			"subtype":   "ACKNOWLEDGED",
			"agent_id":  b["agent_id"],
			"timestamp": time.Now().Format(time.RFC3339),
			"message":   "config change acked",
			"action_id": b["id"],
		})

		revision := int(b["data"].(map[string]interface{})["config"].(map[string]interface{})["revision"].(float64))
		policies.Set(agentName, revision)

		if b["agent_id"].(string) != agent.id {
			panic("agent id mismatch, bummer")
		}
	}

	me := metrics.GetOrRegisterMeter("requests.checkin.success", nil)
	me.Mark(1)

	metrics.GetOrRegisterCounter("actions.received", nil).Inc(int64(len(acks)))

	if len(acks) > 0 {
		reqMap := map[string]interface{}{"events": acks}

		b, err := json.Marshal(reqMap)
		if err != nil {
			return errors.Wrap(err, "marshalling acks")
		}

		req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/api/ingest_manager/fleet/agents/%s/acks", host, agent.id), bytes.NewBuffer(b))
		req.Header.Add("Content-type", "application/json")
		req.Header.Add("Authorization", fmt.Sprintf("ApiKey %s", agent.apiKey))
		req.Header.Add("kbn-xsrf", "false")

		_, err = request(req, "ack")
		if err != nil {
			return errors.Wrap(err, "acking")
		}
		me := metrics.GetOrRegisterMeter("requests.ack.success", nil)
		me.Mark(1)
		metrics.GetOrRegisterCounter("actions.acked", nil).Inc(int64(len(acks)))

		// policy got updated, so notify the global state
		stateMachine.PolicyUpdated()
	}
	/*
	   "action":"checkin","success":true,"actions":[{"agent_id":"11288846-856a-4ac0-9bff-508ea67d6ae0","type":"CONFIG_CHANGE","data":{"config":{"id":"3cd91180-a1b6-11ea-9403-6590bcd4d4db","outputs":{"default":{"type":"elasticsearch","hosts":["http://localhost:9200"],"api_key":"bJBBYXIBWeuISmdwual8:1YWEw5XcTT-KhWkaJrRZLw"}},"datasources":[{"id":"5377a050-a1b6-11ea-9403-6590bcd4d4db","name":"system-1","namespace":"default","enabled":true,"use_output":"default","inputs":[{"type":"logs","enabled":true,"streams":[{"id":"logs-system.auth","enabled":true,"dataset":"system.auth","paths":["/var/log/auth.log*","/var/log/secure*"],"exclude_files":[".gz$"],"multiline":{"pattern":"^\\s","match":"after"},"processors":[{"add_locale":null},{"add_fields":{"target":"","fields":{"ecs.version":"1.5.0"}}}]},{"id":"logs-system.syslog","enabled":true,"dataset":"system.syslog","paths":["/var/log/messages*","/var/log/syslog*"],"exclude_files":[".gz$"],"multiline":{"pattern":"^\\s","match":"after"},"processors":[{"add_locale":null},{"add_fields":{"target":"","fields":{"ecs.version":"1.5.0"}}}]}]},{"type":"system/metrics","enabled":true,"streams":[{"id":"system/metrics-system.core","enabled":true,"dataset":"system.core","metricsets":["core"],"core.metrics":"percentages"},{"id":"system/metrics-system.cpu","enabled":true,"dataset":"system.cpu","metricsets":["cpu"],"core.metrics":"percentages","cpu.metrics":"percentages,normalized_percentages","period":"10s","process.include_top_n.by_cpu":5,"process.include_top_n.by_memory":5,"processes":".*"},{"id":"system/metrics-system.diskio","enabled":true,"dataset":"system.diskio","metricsets":["diskio"]},{"id":"system/metrics-system.entropy","enabled":true,"dataset":"system.entropy","metricsets":["entropy"]},{"id":"system/metrics-system.filesystem","enabled":true,"dataset":"system.filesystem","metricsets":["filesystem"],"period":"1m","processors":[{"drop_event.when.regexp":{"system.filesystem.mount_point":"^/(sys|cgroup|proc|dev|etc|host|lib|snap)($|/)"}}]},{"id":"system/metrics-system.fsstat","enabled":true,"dataset":"system.fsstat","metricsets":["fsstat"],"period":"1m","processors":[{"drop_event.when.regexp":{"system.filesystem.mount_point":"^/(sys|cgroup|proc|dev|etc|host|lib|snap)($|/)"}}]},{"id":"system/metrics-system.load","enabled":true,"dataset":"system.load","metricsets":["load"],"core.metrics":"percentages","cpu.metrics":"percentages,normalized_percentages","period":"10s","process.include_top_n.by_cpu":5,"process.include_top_n.by_memory":5,"processes":".*"},{"id":"system/metrics-system.memory","enabled":true,"dataset":"system.memory","metricsets":["memory"],"core.metrics":"percentages","cpu.metrics":"percentages,normalized_percentages","period":"10s","process.include_top_n.by_cpu":5,"process.include_top_n.by_memory":5,"processes":".*"},{"id":"system/metrics-system.network","enabled":true,"dataset":"system.network","metricsets":["network"],"core.metrics":"percentages","cpu.metrics":"percentages,normalized_percentages","period":"10s","process.include_top_n.by_cpu":5,"process.include_top_n.by_memory":5,"processes":".*"},{"id":"system/metrics-system.network_summary","enabled":true,"dataset":"system.network_summary","metricsets":["network_summary"]},{"id":"system/metrics-system.process","enabled":true,"dataset":"system.process","metricsets":["process"],"core.metrics":"percentages","cpu.metrics":"percentages,normalized_percentages","period":"10s","process.include_top_n.by_cpu":5,"process.include_top_n.by_memory":5,"processes":".*"},{"id":"system/metrics-system.process_summary","enabled":true,"dataset":"system.process_summary","metricsets":["process_summary"],"core.metrics":"percentages","cpu.metrics":"percentages,normalized_percentages","period":"10s","process.include_top_n.by_cpu":5,"process.include_top_n.by_memory":5,"processes":".*"},{"id":"system/metrics-system.raid","enabled":true,"dataset":"system.raid","metricsets":["raid"]},{"id":"system/metrics-system.service","enabled":true,"dataset":"system.service","metricsets":["service"]},{"id":"system/metrics-system.socket","enabled":true,"dataset":"system.socket","metricsets":["socket"]},{"id":"system/metrics-system.socket_summary","enabled":true,"dataset":"system.socket_summary","metricsets":["socket_summary"],"core.metrics":"percentages","cpu.metrics":"percentages,normalized_percentages","period":"10s","process.include_top_n.by_cpu":5,"process.include_top_n.by_memory":5,"processes":".*"},{"id":"system/metrics-system.uptime","enabled":true,"dataset":"system.uptime","metricsets":["uptime"],"core.metrics":"percentages","cpu.metrics":"percentages,normalized_percentages","period":"10s","processes":".*"},{"id":"system/metrics-system.users","enabled":true,"dataset":"system.users","metricsets":["users"]}]}],"package":{"name":"system","version":"0.1.0"}}],"revision":2,"settings":{"monitoring":{"use_output":"default","enabled":true,"logs":true,"metrics":true}}}},"id":"cb59b298-388b-4b76-a924-3974551354c7","created_at":"2020-05-29T16:26:37.191Z"}]}
	*/
	return nil
}

func measureHealthCheck(ctx context.Context, host string, auth string) error {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGUSR1)

	for {
		select {
		case <-time.After(time.Second):
			req, err := http.NewRequestWithContext(ctx, "GET", host+"/api/status", nil)
			req.Header.Add("Content-type", "application/json")
			req.Header.Add("kbn-xsrf", "false")

			splitAuth := strings.Split(auth, ":")
			req.SetBasicAuth(splitAuth[0], splitAuth[1])

			resp, err := request(req, "healthcheck")
			if err == nil {
				if resp.StatusCode < 300 {
					metrics.GetOrRegisterMeter("requests.healthcheck.success", nil).Mark(1)
				} else {
					metrics.GetOrRegisterMeter(fmt.Sprintf("requests.healthcheck.fail.%d", resp.StatusCode), nil).Mark(1)
				}
				continue
			}

			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-c:
			metrics.Unregister("requests.healthcheck.success")
			metrics.Unregister("requests.healthcheck.latency")
			metrics.Unregister("requests.healthcheck.concurrent_count")
			log.Println("reset healthcheck metrics due to SIGUSR1")
		}

	}

}

func printMetrics() {
	scale := time.Millisecond
	du := float64(scale)
	duSuffix := scale.String()[1:]

	metrics.DefaultRegistry.Each(func(name string, i interface{}) {
		switch metric := i.(type) {
		case metrics.Counter:
			log.Printf("counter %s\n", name)
			log.Printf("  count:       %9d\n", metric.Count())
		case metrics.Gauge:
			log.Printf("gauge %s\n", name)
			log.Printf("  value:       %9d\n", metric.Value())
		case metrics.GaugeFloat64:
			log.Printf("gauge %s\n", name)
			log.Printf("  value:       %f\n", metric.Value())
		case metrics.Healthcheck:
			metric.Check()
			log.Printf("healthcheck %s\n", name)
			log.Printf("  error:       %v\n", metric.Error())
		case metrics.Histogram:
			h := metric.Snapshot()
			ps := h.Percentiles([]float64{0.5, 0.75, 0.95, 0.99, 0.999})
			log.Printf("histogram %s\n", name)
			log.Printf("  count:       %9d\n", h.Count())
			log.Printf("  min:         %9d\n", h.Min())
			log.Printf("  max:         %9d\n", h.Max())
			log.Printf("  mean:        %12.2f\n", h.Mean())
			log.Printf("  stddev:      %12.2f\n", h.StdDev())
			log.Printf("  median:      %12.2f\n", ps[0])
			log.Printf("  75%%:         %12.2f\n", ps[1])
			log.Printf("  95%%:         %12.2f\n", ps[2])
			log.Printf("  99%%:         %12.2f\n", ps[3])
			log.Printf("  99.9%%:       %12.2f\n", ps[4])
		case metrics.Meter:
			m := metric.Snapshot()
			log.Printf("meter %s\n", name)
			log.Printf("  count:       %9d\n", m.Count())
			log.Printf("  1-min rate:  %12.2f\n", m.Rate1())
			log.Printf("  5-min rate:  %12.2f\n", m.Rate5())
			log.Printf("  15-min rate: %12.2f\n", m.Rate15())
			log.Printf("  mean rate:   %12.2f\n", m.RateMean())
		case metrics.Timer:
			t := metric.Snapshot()
			ps := t.Percentiles([]float64{0.5, 0.75, 0.95, 0.99, 0.999})
			log.Printf("timer %s\n", name)
			log.Printf("  count:       %9d\n", t.Count())
			log.Printf("  min:         %12.2f%s\n", float64(t.Min())/du, duSuffix)
			log.Printf("  max:         %12.2f%s\n", float64(t.Max())/du, duSuffix)
			log.Printf("  mean:        %12.2f%s\n", t.Mean()/du, duSuffix)
			log.Printf("  stddev:      %12.2f%s\n", t.StdDev()/du, duSuffix)
			log.Printf("  median:      %12.2f%s\n", ps[0]/du, duSuffix)
			log.Printf("  75%%:         %12.2f%s\n", ps[1]/du, duSuffix)
			log.Printf("  95%%:         %12.2f%s\n", ps[2]/du, duSuffix)
			log.Printf("  99%%:         %12.2f%s\n", ps[3]/du, duSuffix)
			log.Printf("  99.9%%:       %12.2f%s\n", ps[4]/du, duSuffix)
			log.Printf("  1-min rate:  %12.2f\n", t.Rate1())
			log.Printf("  5-min rate:  %12.2f\n", t.Rate5())
			log.Printf("  15-min rate: %12.2f\n", t.Rate15())
			log.Printf("  mean rate:   %12.2f\n", t.RateMean())
		}
	})

	revisionSummary := policies.Summary()
	log.Printf("Policy revision summary")
	for k, v := range revisionSummary {
		log.Printf("  revision %2d:   %4d agents\n", k, v)
	}
}

func backoff(ctx context.Context, task string, logger *log.Logger, f func() (interface{}, error)) (interface{}, error) {
	timeout := 5
	for {
		out, err := f()
		if err == context.Canceled {
			return nil, err
		}

		if err == nil {
			return out, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second * time.Duration(timeout+rand.Intn(timeout))):
		}

		timeout = (timeout << 1)
		if timeout > 600 {
			timeout = 600
		}
		log.Printf("%s: err: %s, backoff: %ds\n", task, err, timeout)
	}
}

func runagent(ctx context.Context, agentName string, logger *log.Logger, token, host string) error {
	agentInf, err := backoff(ctx, "enroll", logger, func() (interface{}, error) {
		return enroll(ctx, agentName, host, token)
	})
	if err != nil {
		return errors.Wrap(err, "enrolling")
	}
	agent := agentInf.(agent)
	logger.Printf("%s enrolled..\n", agentName)

	_, err = backoff(ctx, "first checkin", logger, func() (interface{}, error) {
		err = checkin(ctx, agentName, agent, host, true)
		return nil, err
	})
	if err != nil {
		return errors.Wrap(err, "checking in first time")
	}
	logger.Printf("%s checked in first time...\n", agentName)

	// transitioning to long polling
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
			_, err = backoff(ctx, "checkin", logger, func() (interface{}, error) {
				err = checkin(ctx, agentName, agent, host, false)
				return nil, err
			})
			if err != nil {
				return err
			}
			logger.Printf("%s checked in...\n", agentName)
		}
	}
}

func parseENVDuration(envvar string, defaultValue string) (time.Duration, error) {
	val := os.Getenv(envvar)

	if val == "" {
		val = defaultValue
	}

	parsedDuration, err := time.ParseDuration(val)
	if err != nil {
		return 0, errors.Wrapf(err, "parsing %s: %s", envvar, val)
	}

	return parsedDuration, nil
}

func startAgents(ctx context.Context, agentCount int, wg *sync.WaitGroup, enrollDelay time.Duration, logger *log.Logger, token, host string) {
	for i := 0; i < agentCount; i++ {
		j := i // copy is required here
		wg.Add(1)
		go func() {
			agentName := fmt.Sprintf("agent-%d", j)
			err := runagent(ctx, agentName, logger, token, host)
			if err != nil && errors.Cause(err) != context.Canceled {
				fmt.Printf("stopping %s, err: %s\n", agentName, err)
			}
			wg.Done()
		}()

		select {
		case <-ctx.Done():
		case <-time.After(enrollDelay):
		}
	}
}

func main() {
	agents := os.Getenv("AGENTS")
	token := os.Getenv("TOKEN")
	host := os.Getenv("HOST")
	rate := os.Getenv("RATE")
	logLots := os.Getenv("LOG_LOTS")
	auth := os.Getenv("KIBANA_AUTH")
	stopAfter, err := parseENVDuration("STOP_AFTER", "0")
	if err != nil {
		fmt.Printf("err: %s", err)
		return
	}

	metricsInterval, err := parseENVDuration("METRICS_INTERVAL", "30s")
	if err != nil {
		fmt.Printf("err: %s", err)
		return
	}

	if agents == "" || token == "" {
		println("missing AGENTS or TOKEN")
		return
	}

	var logger *log.Logger

	if logLots == "" {
		logger = log.New(ioutil.Discard, "", log.LstdFlags)
	} else {
		logger = log.New(os.Stdout, "", log.LstdFlags)
	}

	if auth == "" {
		auth = "elastic:changeme"
	}

	if host == "" {
		host = "http://localhost:5601"
	}

	agentsi, err := strconv.Atoi(agents)
	if err != nil {
		fmt.Printf("err parsing AGENTS %s: %s", agents, err)
		return
	}

	ratei, err := strconv.Atoi(rate)
	if err != nil {
		fmt.Printf("err parsing rate %s: %s", rate, err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	enrollDelay := time.Second / time.Duration(ratei)
	log.Printf("using enroll delay: %s\n", enrollDelay)

	stateMachine = newTestStateMachine(agentsi)

	go func() {
		for {
			select {
			case state := <-stateMachine.Changed():
				log.Println("state changed:", state)
				printMetrics()
			case <-ctx.Done():
				return
			}
		}
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	wg := sync.WaitGroup{}

	go func() {
		select {
		case <-c:
			fmt.Println("aborting")
			cancel()
		case <-ctx.Done():
		}
	}()

	if stopAfter > 0 {
		go func() {
			select {
			case <-time.After(stopAfter):
				fmt.Printf("stopping after %s\n", stopAfter)
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	if metricsInterval > 0 {
		go func() {
			for {
				time.Sleep(metricsInterval)
				printMetrics()
			}
		}()
	}

	wg.Add(1)
	go func() {
		err := measureHealthCheck(ctx, host, auth)
		if err != context.Canceled {
			log.Printf("healthcheck failed: %v", err)
		}
		wg.Done()
	}()

	startAgents(ctx, agentsi, &wg, enrollDelay, logger, token, host)

	select {
	case <-ctx.Done():
	default:
		log.Printf("agents started...\n")
	}

	<-ctx.Done()

	wg.Wait()
	printMetrics()

}
