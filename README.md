# loadtest

loadtest is a crude load testing tool for fleet/ingest-manager

## Examples:


Enroll 30 agents per second for a total of 5000 agents using the given token. It will use "http://localhost:5601" by default.

NOTE: If you're using SSL, make sure you set HOST accordingly. See below.
```
TOKEN=R2VFd3RYTUJ2TW9WdEtiRy1LXzA6R1I1QnBGb2VRcTI2MHMtX2MxMzhfdw== RATE=30 AGENTS=5000 go run main.go
```

Make it use localhost over SSL:
```
HOST=https://localhost:5601 TOKEN=R2VFd3RYTUJ2TW9WdEtiRy1LXzA6R1I1QnBGb2VRcTI2MHMtX2MxMzhfdw== RATE=30 AGENTS=5000 go run main.go
```

Print metrics every 30s. Set to zero to disable printing metrics.
```
METRICS_INTERVAL=30 TOKEN=... RATE=30 AGENTS=5000 go run main.go
```

Log everything every agent does:
```
LOG_LOTS=1 TOKEN=... RATE=30 AGENTS=5000 go run main.go
```

Print extended metrics:
```
EXTENDED_METRICS=1 TOKEN=... RATE=30 AGENTS=5000 go run main.go
```


### Interpreting the stats

There are 4 types of requests that agents make:
1. enroll
1. first check in - from the Agent perspective, it's just a regular check-in, but we keep it as a separate metric here because they are typically extra heavy
1. ack - acknowledge changes to the configuration that we received in the check in
1. check in - subsequent check-ins to the first one.



500 enrolled and this shows the timings:
```
2020/08/11 13:21:30 timer requests.latency.enroll
2020/08/11 13:21:30   count:             500
2020/08/11 13:21:30   min:               946.74ms
2020/08/11 13:21:30   max:              5880.48ms
2020/08/11 13:21:30   mean:             2370.66ms
2020/08/11 13:21:30   stddev:           1058.24ms
2020/08/11 13:21:30   median:           2084.79ms
2020/08/11 13:21:30   75%:              2751.79ms
2020/08/11 13:21:30   95%:              4970.62ms
2020/08/11 13:21:30   99%:              5745.34ms
2020/08/11 13:21:30   99.9%:            5880.48ms
2020/08/11 13:21:30   1-min rate:          9.89
2020/08/11 13:21:30   5-min rate:         15.17
2020/08/11 13:21:30   15-min rate:        16.24
2020/08/11 13:21:30   mean rate:           8.33
```
Currently zero enrolling requests ongoing
```
2020/08/11 13:21:30 counter requests.concurrent_count.enroll
2020/08/11 13:21:30   count:               0
```
50 successfull ACK requests, 1 per second for the last minute
```
2020/08/11 13:21:30 meter requests.success.ack
2020/08/11 13:21:30   count:              50
2020/08/11 13:21:30   1-min rate:          1.01
2020/08/11 13:21:30   5-min rate:          1.00
2020/08/11 13:21:30   15-min rate:         1.00
2020/08/11 13:21:30   mean rate:           1.08
```
5 completed checkin requests with timings
```
2020/08/11 13:21:30 timer requests.latency.checkin
2020/08/11 13:21:30   count:               5
2020/08/11 13:21:30   min:              1042.71ms
2020/08/11 13:21:30   max:              1058.56ms
2020/08/11 13:21:30   mean:             1049.84ms
2020/08/11 13:21:30   stddev:              5.11ms
2020/08/11 13:21:30   median:           1048.72ms
2020/08/11 13:21:30   75%:              1054.63ms
2020/08/11 13:21:30   95%:              1058.56ms
2020/08/11 13:21:30   99%:              1058.56ms
2020/08/11 13:21:30   99.9%:            1058.56ms
2020/08/11 13:21:30   1-min rate:          0.05
2020/08/11 13:21:30   5-min rate:          0.01
2020/08/11 13:21:30   15-min rate:         0.01
2020/08/11 13:21:30   mean rate:           0.11
```
currently 45 checkin requests ongoing
```
2020/08/11 13:21:30 counter requests.concurrent_count.checkin
2020/08/11 13:21:30   count:              45
```
45 first-checkins with timings (first checkins are more expensive because we have to genrate the API Keys for the config). Only doing half a first check-in per second during the last minute
```
2020/08/11 13:21:30 timer requests.latency.first-checkin
2020/08/11 13:21:30   count:              45
2020/08/11 13:21:30   min:              9144.47ms
2020/08/11 13:21:30   max:             53193.70ms
2020/08/11 13:21:30   mean:            32360.35ms
2020/08/11 13:21:30   stddev:          14049.56ms
2020/08/11 13:21:30   median:          33423.15ms
2020/08/11 13:21:30   75%:             42813.93ms
2020/08/11 13:21:30   95%:             53192.05ms
2020/08/11 13:21:30   99%:             53193.70ms
2020/08/11 13:21:30   99.9%:           53193.70ms
2020/08/11 13:21:30   1-min rate:          0.52
2020/08/11 13:21:30   5-min rate:          0.14
2020/08/11 13:21:30   15-min rate:         0.05
2020/08/11 13:21:30   mean rate:           0.77
```

Currently 455 requests ongoing for first checkin
```
2020/08/11 13:21:30 counter requests.concurrent_count.first-checkin
2020/08/11 13:21:30   count:             455
```
50 successfull subsequent check ins
```
2020/08/11 13:21:30 meter requests.success.checkin
2020/08/11 13:21:30   count:              50
2020/08/11 13:21:30   1-min rate:          1.00
2020/08/11 13:21:30   5-min rate:          1.00
2020/08/11 13:21:30   15-min rate:         1.00
2020/08/11 13:21:30   mean rate:           1.02
```

50 ACKs with timings
```
2020/08/11 13:21:30 timer requests.latency.ack
2020/08/11 13:21:30   count:              50
2020/08/11 13:21:30   min:              1977.94ms
2020/08/11 13:21:30   max:              5035.30ms
2020/08/11 13:21:30   mean:             2430.37ms
2020/08/11 13:21:30   stddev:            874.91ms
2020/08/11 13:21:30   median:           2099.43ms
2020/08/11 13:21:30   75%:              2284.88ms
2020/08/11 13:21:30   95%:              5032.75ms
2020/08/11 13:21:30   99%:              5035.30ms
2020/08/11 13:21:30   99.9%:            5035.30ms
2020/08/11 13:21:30   1-min rate:          1.01
2020/08/11 13:21:30   5-min rate:          1.00
2020/08/11 13:21:30   15-min rate:         1.00
2020/08/11 13:21:30   mean rate:           1.02
```

Currently zero ongoing ACK requests
```
2020/08/11 13:21:30 counter requests.concurrent_count.ack
2020/08/11 13:21:30   count:               0
```


## Interpreting log output

Regular socket timeout happened before fleet send back a response OR you're trying to speak regular HTTP to a HTTPS endpoint. That simulated agent will back off for 10s.
```
2020/08/11 13:24:34 checkin: err: checkin: Post "https://localhost:5601/api/ingest_manager/fleet/agents/99/checkin": EOF, backoff: 10s
```