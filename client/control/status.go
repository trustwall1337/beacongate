package control

import "github.com/trustwall1337/beacongate/client/runtime"

type StatusReport struct {
	ClientID  string `json:"client_id"`
	Connected bool   `json:"connected"`
}

func statusReport(rt *runtime.Runtime) StatusReport {
	return StatusReport{
		ClientID:  rt.ClientID(),
		Connected: true,
	}
}
