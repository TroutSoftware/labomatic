package labomatic

import (
	"encoding/json"
	"fmt"
	"net"
	"runtime"

	"github.com/vishvananda/netns"
)

type QMP struct {
	enc *json.Encoder
	dec *json.Decoder

	net.Conn
}

// Open a QMP socket in network namespace ns, using transport ntw and address addr
func OpenQMP(ns, ntw, addr string) (*QMP, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	nh, err := netns.GetFromName(ns)
	if err != nil {
		return nil, fmt.Errorf("cannot get namespace handle: %w", err)
	}

	if err := netns.Set(nh); err != nil {
		return nil, fmt.Errorf("cannot change network namespace: %w", err)
	}

	sh, err := net.Dial(ntw, addr)
	if err != nil {
		return nil, fmt.Errorf("cannot contact QMP server %s: %w", addr, err)
	}

	return &QMP{
		enc:  json.NewEncoder(sh),
		dec:  json.NewDecoder(sh),
		Conn: sh,
	}, nil
}

func (q QMP) Do(cmd string, args, repl any) error {
	execreq := struct {
		Execute   string `json:"execute"`
		Arguments any    `json:"arguments,omitempty"`
	}{cmd, args}

	q.enc.Encode(execreq)

	var res struct {
		Return json.RawMessage `json:"return"`
		Error  json.RawMessage `json:"error"`
	}
	if err := q.dec.Decode(&res); err != nil {
		return fmt.Errorf("cannot read response: %w", err)
	}
	if len(res.Error) > 0 {
		var err error
		json.Unmarshal(res.Error, &err)
		return err
	}

	if repl == nil {
		return nil
	}

	return json.Unmarshal(res.Return, repl)
}
