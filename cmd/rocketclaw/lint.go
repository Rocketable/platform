package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/Rocketable/platform/internal/rocketclaw/frontend/rpc"
	"github.com/Rocketable/platform/internal/rocketclaw/protocol"
	"google.golang.org/protobuf/encoding/protojson"
)

func runLint(args []string) error {
	output, findings, err := inspectAgents("lint", protocol.InspectLint, args)
	if err != nil {
		return err
	}

	if err := printStdout(output, "lint result"); err != nil {
		return err
	}

	if findings > 0 {
		return exitCodeError(1)
	}

	return nil
}

func runSettings(args []string) error {
	settings, _, err := inspectAgents("settings", protocol.InspectSettings, args)
	if err != nil {
		return err
	}

	return printStdout(settings, "settings")
}

func inspectAgents(name string, kind protocol.InspectKind, args []string) (string, int, error) {
	flagSet := flag.NewFlagSet("rocketclaw "+name, flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	arn := flagSet.String(secretsARNFlag, "", secretsARNUsage)
	if err := flagSet.Parse(args); err != nil {
		return "", 0, fmt.Errorf("parse %s flags: %w", name, err)
	}
	rest, target, usage := flagSet.Args(), protocol.InspectNext, "usage: rocketclaw "+name+" [next|current]"
	if kind == protocol.InspectSettings {
		target, usage = protocol.InspectCurrent, "usage: rocketclaw settings"
	} else if len(rest) > 0 {
		target, rest = protocol.InspectTarget(rest[0]), rest[1:]
	}
	if len(rest) > 0 || (target != protocol.InspectNext && target != protocol.InspectCurrent) {
		return "", 0, errors.New(usage)
	}

	_, cfg, err := loadRuntimeConfig(*arn)
	if err != nil {
		return "", 0, fmt.Errorf("load config: %w", err)
	}

	_, port, err := net.SplitHostPort(cfg.Web.ListenAddress)
	if err != nil {
		return "", 0, fmt.Errorf("read web.listen_address: %w", err)
	}

	address := net.JoinHostPort("127.0.0.1", port)
	body, err := protojson.Marshal(&rpc.InspectAgentsRequest{Target: string(target), Kind: string(kind)})
	if err != nil {
		return "", 0, fmt.Errorf("encode %s request: %w", name, err)
	}

	// The server's certificate may be self-signed anew at each start, and loopback never leaves this host.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	response, err := client.Post("https://"+address+"/api/InspectAgents", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("could not reach the running rocketclaw server at %s: %w", address, err)
	}
	defer func() { _ = response.Body.Close() }()

	var result struct {
		Output   string `json:"output"`
		Findings int    `json:"findings"`
		Message  string `json:"message"`
	}
	errDecode := json.NewDecoder(response.Body).Decode(&result)
	if response.StatusCode != http.StatusOK {
		// An older server answers a route it does not know with plain text, not {code,message}.
		if errDecode != nil || result.Message == "" {
			return "", 0, errors.New("the running rocketclaw server does not support this command; restart it after upgrading")
		}
		return "", 0, fmt.Errorf("rocketclaw server at %s failed %s: %s", address, name, result.Message)
	}
	if errDecode != nil {
		return "", 0, fmt.Errorf("read rocketclaw server %s answer: %w", name, errDecode)
	}

	return result.Output, result.Findings, nil
}
