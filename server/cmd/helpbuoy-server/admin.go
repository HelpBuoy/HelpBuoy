package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// adminCommands are the commands a request on the admin socket may run. serve
// and admin itself are left out.
var adminCommands = map[string]bool{
	"techie": true, "computer": true, "customer": true, "info": true, "limit": true,
	"hostkey": true, "vhost": true, "log": true, "version": true,
}

// maxAdminRequest is the most a request may hold, in bytes.
const maxAdminRequest = 64 << 10

// adminReply is what the admin command answers: the exit code and what the
// command printed on stdout and stderr together.
type adminReply struct {
	Exit   int    `json:"exit"`
	Output string `json:"output"`
}

// adminCmd runs one admin command for a program that may not run
// helpbuoy-server itself, like a web admin under another user. systemd starts
// it for every connection on the admin socket (helpbuoy-server-admin.socket),
// as the user of the server, with the connection as stdin and stdout.
//
// The request is one line with a JSON array of arguments, like
// ["techie","list","--vhosts","/var/lib/helpbuoy-server/vhosts","--vhost","acme.example.com"],
// and the reply one line {"exit":0,"output":"..."}. Only the commands in
// adminCommands run, and only on the state directories the service names:
// --vhosts must be $HELPBUOY_SERVER_VHOSTS or $HELPBUOY_SERVER_STOPPED,
// --stopped must be $HELPBUOY_SERVER_STOPPED, and --state is refused.
func adminCmd(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("admin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("%w: admin (reads a request on stdin)", errUsage)
	}
	line, err := bufio.NewReaderSize(io.LimitReader(stdin, maxAdminRequest), 4096).ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return reply(stdout, adminReply{Exit: 2, Output: "helpbuoy-server: no request; send one line with a JSON array of arguments"})
	}
	var req []string
	if err := json.Unmarshal(line, &req); err != nil {
		return reply(stdout, adminReply{Exit: 2, Output: "helpbuoy-server: the request is not a JSON array of strings"})
	}
	if err := checkAdmin(req); err != nil {
		return reply(stdout, adminReply{Exit: 2, Output: "helpbuoy-server: " + err.Error()})
	}
	var out bytes.Buffer
	code := run(req, &out, &out)
	return reply(stdout, adminReply{Exit: code, Output: out.String()})
}

func reply(w io.Writer, r adminReply) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// checkAdmin checks that a request runs an allowed command on the
// directories of the service. It looks at every argument that could be read
// as one of the directory flags, also after a "--", so it never lets one
// through that the command's own flag parsing would accept.
func checkAdmin(args []string) error {
	if len(args) == 0 || !adminCommands[args[0]] {
		return errors.New("command not allowed on the admin socket")
	}
	dirs := func(envs ...string) map[string]bool {
		m := map[string]bool{}
		for _, env := range envs {
			if d := os.Getenv(env); d != "" {
				m[filepath.Clean(d)] = true
			}
		}
		return m
	}
	allowed := map[string]map[string]bool{
		"vhosts":  dirs("HELPBUOY_SERVER_VHOSTS", "HELPBUOY_SERVER_STOPPED"),
		"stopped": dirs("HELPBUOY_SERVER_STOPPED"),
	}
	for i := 1; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if name == "state" {
			return errors.New("--state is not allowed on the admin socket")
		}
		ok, isDir := allowed[name]
		if !isDir {
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return fmt.Errorf("--%s without a directory", name)
			}
			i++
			value = args[i]
		}
		if value == "" || !ok[filepath.Clean(value)] {
			return fmt.Errorf("--%s %s is not a directory of this server", name, value)
		}
	}
	return nil
}
