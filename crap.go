package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/brettbuddin/campfire"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	configurationFile = "crap.json"
	version           = "0.9"
)

var (
	crapify      = flag.Bool("crapify", false, "create a new configuration file")
	printVersion = flag.Bool("version", false, "print version and exit")
	verbose      = flag.Bool("verbose", false, "verbose logging")
)

func main() {
	deployStart := time.Now()

	flag.Parse()

	runtime.GOMAXPROCS(runtime.NumCPU())
	if *verbose {
		log.Println("GOMAXPROCS", runtime.GOMAXPROCS(0))
	}

	if *printVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	if *crapify {
		createSampleConfig()
		os.Exit(0)
	}

	conf := parseConfig()
	conf.validate()

	env := conf.selectEnvironment()
	env.validate()

	cmd := "if [ ! -d out ]; then mkdir -p out; fi"
	run("create out dir", exec.Command("/bin/sh", "-c", cmd))

	server := &(env.Servers[0])

	controlMasterStarted := server.startSSHControlMaster()
	defer func() {
		server.stopSSHControlMaster()
	}()

	appBuildReady := build(conf.AppBuildCommands)
	assetBuildReady := build(conf.AssetBuildCommands)

	releaseBasePath := filepath.Join(env.DeployDir, "releases")
	releaseDir := filepath.Join(releaseBasePath, time.Now().Format("20060102150405"))

	serverPrepared := server.prepareServer(env, releaseDir, len(conf.AssetBuildCommands) > 0)

	<-controlMasterStarted
	<-serverPrepared

	assetsRsynced := make(chan bool)
	go func() {
		<-assetBuildReady
		<-server.rsyncAssets(env, conf)
		assetsRsynced <- true
	}()

	appUploaded := make(chan bool)
	go func() {
		<-appBuildReady
		<-server.uploadApp(env)
		appUploaded <- true
	}()

	appFinalized := make(chan bool)
	go func() {
		<-appUploaded
		<-assetsRsynced
		<-server.finalizeApp(env, releaseDir)
		appFinalized <- true
	}()

	<-appFinalized

	if len(env.AfterDeployCommand) > 0 {
		run("After deploy hook", exec.Command("sh", "-c", env.AfterDeployCommand))
	}

	deployDuration := time.Since(deployStart)
	log.Println("app deployed to", env.Name, "("+deployDuration.String()+")")

	if conf.Campfire.Account != "" && conf.Campfire.Token != "" && conf.Campfire.Rooms != "" {
		err := announceInCampfire(conf.Campfire, env.Name, deployDuration)
		if err != nil {
			panic(err)
		}
	}
}

func announceInCampfire(account campfireAccount, environmentName string, deployDuration time.Duration) error {
	currentUser, err := user.Current()
	if err != nil {
		return err
	}
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var roomIDs []int
	for _, s := range strings.Split(account.Rooms, ",") {
		id, err := strconv.Atoi(s)
		if err != nil {
			return err
		}
		roomIDs = append(roomIDs, id)
	}
	client := campfire.NewClient(account.Account, account.Token)
	rooms, err := client.Rooms()
	if err != nil {
		return err
	}
	roomMap := make(map[int]*campfire.Room)
	for _, room := range rooms {
		roomMap[room.Id] = room
	}
	for _, id := range roomIDs {
		room, found := roomMap[id]
		if !found {
			return fmt.Errorf("Room %d not found", id)
		}
		room.SendText(fmt.Sprintf("%s deployed %s to %s in %v",
			currentUser.Username, filepath.Base(pwd), environmentName, deployDuration))
	}
	return nil
}

func run(label string, cmd *exec.Cmd) {
	cmdStart := time.Now()
	if *verbose {
		args := strings.Join(cmd.Args, " ")
		log.Println(label, "args:", args)
	}
	if err := cmd.Run(); err != nil {
		args := strings.Join(cmd.Args, " ")
		log.Println("Error!", label, args, err.Error())
		os.Exit(1)
	}
	since := time.Since(cmdStart)
	log.Println(label, "("+since.String()+")")
}

func (s *server) startSSHControlMaster() chan bool {
	done := make(chan bool)
	go func() {
		cmd := fmt.Sprintf("ssh -nNf -o \"ControlMaster=yes\" -o \"ControlPath=%s\" -p %s %s", s.Socket(), s.Port, s.Host())
		run("start SSH ControlMaster", exec.Command("sh", "-c", cmd))
		done <- true
	}()
	return done
}

func (s *server) finalizeApp(env *environment, releaseDir string) chan bool {
	done := make(chan bool)
	go func() {
		var finalize bytes.Buffer

		// Copy app files into release dir
		cmd := fmt.Sprintf("cp -r %s %s", filepath.Join(env.DeployDir, "shared", "dist", "*"), releaseDir)
		finalize.WriteString(cmd)

		// Replace symlink
		symlink := filepath.Join(env.DeployDir, "current")
		finalize.WriteString(fmt.Sprintf(" && rm -f %s", symlink))
		finalize.WriteString(fmt.Sprintf(" && ln -s %s %s", releaseDir, symlink))

		if len(env.RestartCommand) > 0 {
			cmd := fmt.Sprintf(" && (%s)", env.RestartCommand)
			finalize.WriteString(cmd)
		}

		run("finalize server", exec.Command("ssh", "-p", s.Port, "-S", fmt.Sprintf("'%s'", s.Socket()), "-l", s.User, s.IP, finalize.String()))

		done <- true
	}()
	return done
}

func (s *server) uploadApp(env *environment) chan bool {
	done := make(chan bool)
	go func() {
		cmd := fmt.Sprintf("rsync -e 'ssh -p %s -S \"%s\"' --recursive --times --compress --human-readable dist %s:%s/shared",
			s.Port, s.Socket(), s.Host(), env.DeployDir)
		run("rsync app files", exec.Command("/bin/sh", "-c", cmd))
		done <- true
	}()
	return done
}

func (s *server) prepareServer(env *environment, releaseDir string, hasBuildCommands bool) chan bool {
	done := make(chan bool)
	go func() {
		// Run a bunch of commands on the remote server. Set up shared dir, symlinks etc
		var buffer bytes.Buffer

		cmd := fmt.Sprintf("if [ ! -d %s/shared/log ]; then mkdir -p %s/shared/log; fi", env.DeployDir, env.DeployDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && if [ ! -d %s ]; then mkdir -p %s; fi", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && chmod -R g+w %s", releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && (rm -rf %s/public/system || true) && mkdir -p %s/public/", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/system %s/public/system", env.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && rm -rf %s/log", releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/log %s/log", env.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && rm -rf %s/tmp/pids && mkdir -p %s/tmp/", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/pids %s/tmp/pids", env.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		if hasBuildCommands {
			cmd = fmt.Sprintf(" && rm -rf %s/public/assets && mkdir -p %s/public && mkdir -p %s/shared/assets && ln -s %s/shared/assets %s/public/assets",
				releaseDir, releaseDir, env.DeployDir, env.DeployDir, releaseDir)
			buffer.WriteString(cmd)
		}

		run("prepare server", exec.Command("ssh", "-p", s.Port, "-S", s.Socket(), "-l", s.User, s.IP, buffer.String()))
		done <- true
	}()
	return done
}

func createSampleConfig() {
	conf := newSampleConfiguration()
	b, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := ioutil.WriteFile(configurationFile, b, 0644); err != nil {
		panic(err)
	}
}

func parseConfig() *configuration {
	b, err := ioutil.ReadFile(configurationFile)
	if err != nil {
		// Try config subfolder
		b, err = ioutil.ReadFile(filepath.Join("config", configurationFile))
		if err != nil {
			fmt.Println("Could not open configuration file (crap.json or config/crap.json):", err)
			fmt.Println("Hint: pass --crapify to create a new configuration file")
			os.Exit(1)
		}
	}
	var conf configuration
	if err := json.Unmarshal(b, &conf); err != nil {
		panic(err)
	}
	return &conf
}

func (conf *configuration) selectEnvironment() *environment {
	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Specify an environment")
		os.Exit(1)
	}
	var env *environment
	for _, e := range conf.Environments {
		if e.Name == args[0] {
			env = &e
			break
		}
	}
	if env == nil {
		fmt.Println("Environment", args[0], "not found")
		os.Exit(1)
	}
	return env
}

func (env *environment) validate() {
	if len(env.DeployDir) == 0 {
		fmt.Println("deploydir must be filled in!")
		os.Exit(1)
	}
	if len(env.Servers) != 1 {
		fmt.Println("No server(s) in environment configuration. Please specify one!")
		os.Exit(1)
	}
}

func (conf *configuration) validate() {
	if len(conf.AssetBuildCommands) == 0 && len(conf.AppBuildCommands) == 0 {
		fmt.Println("No asset_build_commands or app_build_commands found in environment configuration")
		os.Exit(1)
	}
	if len(conf.CrapVersion) == 0 {
		fmt.Println("Your crap.json file is unversioned - please add crap_version to your crap.json")
		os.Exit(1)
	}
	if conf.CrapVersion != version {
		fmt.Println("Your crap.json requires crap", conf.CrapVersion, "but this crap is", version, "- please upgrade!")
		os.Exit(1)
	}
}

func (s *server) stopSSHControlMaster() {
	cmd := fmt.Sprintf("ssh -O exit -S '%s' -p %s %s", s.Socket(), s.Port, s.Host())
	run("stop SSH ControlMaster", exec.Command("sh", "-c", cmd))
}

func (s *server) rsyncAssets(env *environment, conf *configuration) chan bool {
	done := make(chan bool)
	go func() {
		if len(conf.AssetBuildCommands) > 0 {
			cmd := fmt.Sprintf("rsync -e 'ssh -p %s -S \"%s\"' --recursive --times --compress --human-readable public/assets %s:%s/shared",
				s.Port, s.Socket(), s.Host(), env.DeployDir)
			run("rsync assets", exec.Command("/bin/sh", "-c", cmd))
		}
		done <- true
	}()
	return done
}

func build(buildCommands []string) chan bool {
	done := make(chan bool)
	buildOne := func(buildCommand string, done chan bool) {
		run(buildCommand, exec.Command("sh", "-c", buildCommand))
		done <- true
	}
	go func() {
		builds := make(chan bool, len(buildCommands))
		for _, cmd := range buildCommands {
			go buildOne(cmd, builds)
		}
		for _ = range buildCommands {
			<-builds
		}
		done <- true
	}()
	return done
}
