// Package machines is what Muster keeps of each machine that reports: the
// device its certificate names, and its last report from the Muster reader.
package machines

import (
	"time"

	"github.com/orchard-suite/muster/reader"
)

// Device is the machine and its person, as the door read them from its
// certificate.
type Device struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Person      string    `json:"person"`
	Serial      string    `json:"serial"`
	Fingerprint string    `json:"fingerprint"`
	NotAfter    time.Time `json:"not_after"`
}

// Report is one device's last report: who sent it, when it says it was
// read, when it arrived, and what it holds.
type Report struct {
	Device   Device        `json:"device"`
	At       time.Time     `json:"at"`
	Received time.Time     `json:"received"`
	Report   reader.Report `json:"report"`
}
