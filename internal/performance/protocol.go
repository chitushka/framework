package performance

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
)

//go:embed protocol.json
var protocolJSON []byte

type Protocol struct {
	Version string `json:"version"`
	Bench   struct {
		Time   string `json:"time"`
		Count  int    `json:"count"`
		CPUs   []int  `json:"cpus"`
		Routes []int  `json:"routes"`
	} `json:"bench"`
	SQL struct {
		Rows      int `json:"rows"`
		NameBytes int `json:"nameBytes"`
		Pool      int `json:"pool"`
	} `json:"sql"`
	Soak struct {
		Rate            int     `json:"rate"`
		Inflight        int     `json:"inflight"`
		SQLBudgetMS     int     `json:"sqlBudgetMS"`
		ClientTimeoutMS int     `json:"clientTimeoutMS"`
		SampleMS        int     `json:"sampleMS"`
		CleanupMS       int     `json:"cleanupMS"`
		Phases          []Phase `json:"phases"`
		Budget          Budget  `json:"budget"`
	} `json:"soak"`
}

type Phase struct {
	Name    string `json:"name"`
	Seconds int    `json:"seconds"`
}

type Budget struct {
	SuccessMinPercent    float64 `json:"successMinPercent"`
	DropMaxPercent       float64 `json:"dropMaxPercent"`
	Fault503MinPercent   float64 `json:"fault503MinPercent"`
	UnexpectedMaxPercent float64 `json:"unexpectedMaxPercent"`
	P99MS                float64 `json:"p99MS"`
	HeapGrowthBytes      int64   `json:"heapGrowthBytes"`
	SlopeBytesPerMinute  float64 `json:"slopeBytesPerMinute"`
	Goroutines           int     `json:"goroutines"`
}

func LoadProtocol() (Protocol, error) {
	var p Protocol
	err := json.Unmarshal(protocolJSON, &p)
	return p, err
}

func ProtocolHash() string {
	hash := sha256.Sum256(bytes.ReplaceAll(protocolJSON, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(hash[:])
}
