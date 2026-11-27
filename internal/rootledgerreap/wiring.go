package rootledgerreap

import "errors"

// RootOwned checks a helper is root-owned and not writable; the broker wires
// its own check, unwired it refuses.
var RootOwned = func(string) error { return errors.New("rootledgerreap: helper check not wired") }
