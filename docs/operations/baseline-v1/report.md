# RC operational full

Accepted: **true**. Candidate v0.1.0-rc.1, source `90b2c3bf936c5d7f67eb2a4b9216d438b1f40739`.

Harness commit `d746704b21a4d260d8e995b22e8c9668e478fa3e`; protocol SHA-256 `d6051561501132ede393ee1b66c9f7d9a2f3ca6a007aad0dbebd889c5786fe78`.

| Phase | Seconds | Requests | Errors | Error rate | p99 ms | Lag p99 ms |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| baseline | 60 | 1200 | 0 | 0.000% | 132.54 | 14.85 |
| rolling | 60 | 1200 | 1 | 0.083% | 168.47 | 14.98 |
| rotation | 60 | 1200 | 1 | 0.083% | 156.20 | 14.83 |
| sql-outage | 60 | 1200 | 793 | 66.083% | 757.72 | 14.92 |
| cache-outage | 60 | 1200 | 443 | 36.917% | 306.67 | 14.75 |
| queue-outage | 90 | 1800 | 0 | 0.000% | 159.78 | 15.02 |
| app-crash | 60 | 1200 | 1 | 0.083% | 448.44 | 14.90 |
| final | 150 | 3000 | 0 | 0.000% | 131.96 | 14.71 |

Total: 12000 requests, error rate 10.325%, p99 753.18ms (faults included).

Resources: 150 samples, max RSS 17.83MiB, heap 3.41MiB, goroutines 37, SQL pool 6.

- rolling: {"name":"rolling","passed":true,"durationMS":24905.751000000004,"additiveMigration":true,"oldRevisionRollback":true,"drainMS":2217.379099999991}
- rotation: {"name":"rotation","passed":true,"durationMS":4077.58719999998,"overlapBothAccepted":true,"retiredKeyRejected":true,"tlsBefore":"67:6A:68:C2:89:1E:A7:F7:44:E1:55:87:31:2F:1D:D3:73:3C:4E:37:40:A0:39:34:21:16:EB:50:EC:FB:FD:4C","tlsAfter":"11:38:95:0C:57:8D:4E:D7:63:21:26:61:E3:85:A8:A3:56:D7:B9:57:C3:E6:FE:1D:08:2E:A3:FF:9F:93:27:20","caVerified":true}
- sql-outage: {"name":"sql-outage","passed":true,"durationMS":41025.3462,"downtimeMS":15000,"recoveryMS":24782.67590000003,"sameVolume":true}
- cache-outage: {"name":"cache-outage","passed":true,"durationMS":23610.1556,"downtimeMS":15000,"recoveryMS":3845.031100000022,"coldCache":true}
- queue-outage: {"name":"queue-outage","passed":true,"durationMS":37033.58779999998,"downtimeMS":15000,"recoveryMS":20288.466199999966,"confirmedBeforeRestart":5097}
- app-crash: {"name":"app-crash","passed":true,"durationMS":5116.356800000009,"recoveryMS":6.131600000022445,"restartMS":2335.881700000027,"secondReplicaSurvived":true}
- reconcile: {"name":"reconcile","passed":true,"durationMS":467.35009999992326,"accepted":10761,"persisted":10763,"ambiguousCommitted":2,"effectsExactlyOne":true,"duplicateIDChecked":true}
- backup-restore: {"name":"backup-restore","passed":true,"durationMS":10412.940899999929,"rtoMS":5852.491700000013,"rpoOperations":0,"rowsSHA256":"f0dd5872c5cabc1acbf9178356a56d1edb41b4dfaa44c61ba4cb690be6285c20","dumpSHA256":"7f39332ff903b3b170db684ef54bdfe7038c00894f7b7fce6a2a4f293ac7c191","bytes":35037,"newDatabase":true,"restoredTLSApp":true}
- migration-recovery: {"name":"migration-recovery","passed":true,"durationMS":9260.774099999922,"recoveryMS":4702.634300000034,"dirtyBlocked":true,"inspectedSchema":true,"exactChecksumResolve":true,"repeatedUpNoop":true,"terminatedSleepingSession":true}
- shutdown: {"name":"shutdown","passed":true,"durationMS":2025.2086000000127,"gracefulExitZero":true,"noOOM":true}
