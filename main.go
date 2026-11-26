// Command ansibleforms-rte-builder builds the RTE images an AnsibleForms controller defines (its
// Designer's RTE images) and pushes them to their registry. It dials the controller and keeps
// one WebSocket open (internal/socket), is handed one build at a time (internal/builder), builds
// with BuildKit (internal/buildkit) and signs with the team's cosign key (internal/sign). It
// never reads the controller's database nor its repositories, and runs no playbook.
//
//	ansibleforms-rte-builder [command]
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ansibleforms/builder/internal/builder"
	"github.com/ansibleforms/builder/internal/buildkit"
	"github.com/ansibleforms/builder/internal/logger"
	"github.com/ansibleforms/builder/internal/settings"
	"github.com/ansibleforms/builder/internal/shutdown"
	"github.com/ansibleforms/builder/internal/socket"
	"github.com/ansibleforms/builder/internal/version"
)

// usage lists the commands (--help). The builder takes its settings from its environment
// (.env.example lists them), so it has no flags.
func usage() {
	fmt.Fprint(os.Stderr, `AnsibleForms RTE Builder

  ansibleforms-rte-builder [command]

Commands:
  run      dial the controller and build what it hands over (default)
  health   exit 0 when its probes answer: the image's HEALTHCHECK
  version  print its version

Its settings are environment variables: CONTROLLER_URL, RTE_TOKEN, RTE_JOIN_TOKEN, RTE_NAME,
BUILDKIT_HOST, BUILDER_WORK_PATH, PORT and LOG_LEVEL.
`)
}

// What main and run call, the tests replace: the process's exit, its own buildkitd, how often
// it sends its health, and what run waits on once started. Nothing closes stopped in the
// process: SIGTERM runs the closers of internal/shutdown, which end it.
var (
	exit          = os.Exit
	startBuildkit = buildkit.Start
	healthEvery   = 20 * time.Second
	stopped       = make(chan struct{})
)

func main() {
	exit(command(os.Args[1:]))
}

// command runs the command its arguments name.
//
// Args:
//
//	args: the arguments, without the program's name.
//
// Returns:
//
//	The exit code: 0 done, 1 unhealthy, 2 a command or flag it does not know ; run never
//	returns.
func command(args []string) int {
	flags := flag.NewFlagSet("ansibleforms-rte-builder", flag.ContinueOnError)
	flags.Usage = usage
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	name := "run"
	if flags.NArg() > 0 {
		name = flags.Arg(0)
	}
	switch name {
	case "run":
		return run()
	case "health":
		return health()
	case "version":
		fmt.Println(version.App)
		return 0
	}
	fmt.Fprintf(os.Stderr, "ansibleforms-rte-builder: '%s' is no command\n\n", name)
	usage()
	return 2
}

// health is the image's HEALTHCHECK: healthy when its port takes a connection.
//
// Returns:
//
//	The exit code: 0 healthy, 1 not.
func health() int {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(settings.Port())), 4500*time.Millisecond)
	if err != nil {
		return 1
	}
	_ = conn.Close()
	return 0
}

// run starts the builder and keeps it running until SIGTERM or SIGINT.
//
// Returns:
//
//	1 when it cannot start ; otherwise 0, once stopped is closed (in the tests only).
func run() int {
	shutdown.Install()
	// step 1 : its settings, or why it cannot start
	if problems := settings.Problems(); len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "Builder: %s ; refusing to start\n", strings.Join(problems, " ; "))
		return 1
	}
	if err := os.MkdirAll(filepath.Join(settings.WorkDir(), "spool"), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "Builder: its work folder cannot be made (%v) ; refusing to start\n", err)
		return 1
	}

	// step 2 : the builds an earlier start left, and its BuildKit
	b := builder.New()
	if err := b.ResumeSpools(); err != nil {
		logger.Error("Builder: the spools of an earlier start cannot be read: %v", err)
	}
	stopBuildkit, _ := startBuildkit()
	shutdown.OnShutdown("buildkitd", stopBuildkit)

	// step 3 : its container's probes : alive, and ready once the controller welcomed it
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/live" || (r.URL.Path == "/ready" && b.IsWelcomed()) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
	})
	server := &http.Server{Addr: net.JoinHostPort(settings.ListenHost(), strconv.Itoa(settings.Port())), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Builder: its probes cannot listen: %v", err)
		}
	}()
	shutdown.OnShutdown("listener", server.Close)

	// step 4 : its health every 20 seconds, and its dial
	ticker, done := time.NewTicker(healthEvery), stopped
	go func() {
		for {
			select {
			case <-ticker.C:
				b.SendHealth()
			case <-done:
				return
			}
		}
	}()
	logger.Notice("Builder '%s' %s starting, BuildKit at %s", settings.BuilderName(), version.App, settings.BuildkitHost())
	dialer, err := socket.DialOut(settings.Token(), b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Builder: %v ; refusing to start\n", err)
		return 1
	}
	shutdown.OnShutdown("socket", dialer.Stop)
	<-done
	// released (in the tests only) : what it started stops, its dial last
	ticker.Stop()
	_ = server.Close()
	_ = dialer.Stop()
	<-dialer.Done()
	return 0
}
