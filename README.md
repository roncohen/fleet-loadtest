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

Print metrics every 30s:
```
METRICS_INTERVAL=30 TOKEN=... RATE=30 AGENTS=5000 go run main.go
```


