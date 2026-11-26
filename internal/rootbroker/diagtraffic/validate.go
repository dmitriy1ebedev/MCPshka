package diagtraffic

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

const RunsRoot = "/var/lib/diag/traffic-runs"

func Validate(args []string) error {
	if len(args) == 0 {
		return errors.New("missing diag traffic operation")
	}
	switch args[0] {
	case "start":
		if len(args) != 9 ||
			args[1] != "--client" ||
			args[3] != "--mac" ||
			args[5] != "--duration" ||
			args[7] != "--root" ||
			filepath.Clean(args[8]) != RunsRoot {
			return errors.New("unsupported diag traffic start form")
		}
		_, err := parseTarget(args[2], args[4], args[6])
		return err
	case "status", "report", "stop":
		if len(args) != 6 ||
			args[1] != "--run" ||
			!validDiagTrafficRunID(args[2]) ||
			args[3] != "--root" ||
			filepath.Clean(args[4]) != RunsRoot ||
			args[5] != "--json" {
			return errors.New("unsupported diag traffic read form")
		}
		return nil
	default:
		return fmt.Errorf("unsupported diag traffic operation %q", args[0])
	}
}

func validDiagTrafficRunID(v string) bool {
	if len(v) != 25 || v[8] != 'T' || v[15] != 'Z' || v[16] != '-' {
		return false
	}
	for i := 0; i < 16; i++ {
		if i == 8 || i == 15 {
			continue
		}
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	for i := 17; i < len(v); i++ {
		if !((v[i] >= '0' && v[i] <= '9') || (v[i] >= 'a' && v[i] <= 'f')) {
			return false
		}
	}
	return true
}

type target struct {
	client, mac string
	duration    int
}

func parseTarget(client, address, duration string) (target, error) {
	ip := net.ParseIP(client)
	if strings.Contains(client, ":") || ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		return target{}, errors.New("diag traffic client must be private unicast IPv4")
	}
	mac, err := net.ParseMAC(address)
	if err != nil || len(address) != 17 || strings.Count(address, ":") != 5 || len(mac) != 6 ||
		mac[0]&1 != 0 || bytes.Count(mac, []byte{0}) == 6 {
		return target{}, errors.New("invalid diag traffic MAC")
	}
	seconds, err := strconv.Atoi(duration)
	if err != nil || seconds < 10 || seconds > 300 {
		return target{}, errors.New("diag traffic duration must be 10..300 seconds")
	}
	return target{client: ip.String(), mac: mac.String(), duration: seconds}, nil
}
