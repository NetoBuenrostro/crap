package main

import (
	"fmt"
)

type (
	Server struct {
		Port string `json:"port"`
		User string `json:"user"`
		Ip   string `json:"ip"`
	}
	Environment struct {
		Name               string   `json:"name"`
		Servers            []Server `json:"servers"`
		RestartCommand     string   `json:"restart_command"`
		DeployDir          string   `json:"deploydir"`
		AfterDeployCommand string   `json:"after_deploy_command"`
	}
	Campfire struct {
		Account string `json:"account"`
		Token   string `json:"token"`
		Rooms   string `json:"rooms"`
	}
	Configuration struct {
		CrapVersion        string        `json:"crap_version"`
		Environments       []Environment `json:"environments"`
		BuiltAppDir        string        `json:"built_app_dir"`
		AppBuildCommands   []string      `json:"app_build_commands"`
		AssetBuildCommands []string      `json:"asset_build_commands"`
		Campfire           Campfire
	}
)

func NewSampleConfiguration() *Configuration {
	return &Configuration{
		CrapVersion: Version,
		Environments: []Environment{
			Environment{
				Name: "staging",
				Servers: []Server{
					Server{Port: "22", User: "deployment", Ip: "127.0.0.1"},
					Server{Port: "22", User: "deployment", Ip: "localhost"}},
				RestartCommand: "(sudo stop myapp_staging || true) && sudo start myapp_staging",
				DeployDir:      "/var/www/myapp"},
			Environment{
				Name: "production",
				Servers: []Server{
					Server{Port: "22", User: "deployment", Ip: "www.myapp.com"}},
				DeployDir:          "/var/www/myapp",
				RestartCommand:     "(sudo stop myapp_production || true) && sudo start myapp_production",
				AfterDeployCommand: "make cleanup"}},
		AppBuildCommands:   []string{"make linux64bit"},
		AssetBuildCommands: []string{"make css_assets_gzip", "make js_assets_gzip"},
		BuiltAppDir:        "out/myapp",
		Campfire:           Campfire{Account: "mycampfireaccount", Token: "foobarfoobarfoobar", Rooms: "8343,234223"},
	}
}

func (server *Server) Host() string {
	return fmt.Sprintf("%s@%s", server.User, server.Ip)
}

func (server *Server) Socket() string {
	return fmt.Sprintf("%s:%s", server.Host(), server.Port)
}
