package main

import (
	"fmt"
)

type (
	server struct {
		Port string `json:"port"`
		User string `json:"user"`
		IP   string `json:"ip"`
	}
	environment struct {
		Name               string   `json:"name"`
		Servers            []server `json:"servers"`
		RestartCommand     string   `json:"restart_command"`
		DeployDir          string   `json:"deploydir"`
		AfterDeployCommand string   `json:"after_deploy_command"`
		Default            bool     `json:"default"`
	}
	configuration struct {
		CrapVersion        string        `json:"crap_version"`
		Environments       []environment `json:"environments"`
		BuiltAppDir        string        `json:"built_app_dir"`
		AppBuildCommands   []string      `json:"app_build_commands"`
		AssetBuildCommands []string      `json:"asset_build_commands"`
	}
)

func newSampleConfiguration() *configuration {
	return &configuration{
		CrapVersion: version,
		Environments: []environment{
			environment{
				Name: "staging",
				Servers: []server{
					server{
						Port: "22",
						User: "deployment",
						IP:   "localhost",
					},
				},
				RestartCommand: "(sudo stop myapp_staging || true) && sudo start myapp_staging",
				DeployDir:      "/var/www/myapp"},
			environment{
				Name: "production",
				Servers: []server{
					server{
						Port: "22",
						User: "deployment",
						IP:   "www.myapp.com",
					},
				},
				DeployDir:          "/var/www/myapp",
				RestartCommand:     "(sudo stop myapp_production || true) && sudo start myapp_production",
				AfterDeployCommand: "make clean",
			},
		},
		AppBuildCommands:   []string{"make linux64bit"},
		AssetBuildCommands: []string{"make css_assets_gzip", "make js_assets_gzip"},
		BuiltAppDir:        "dist",
	}
}

func (s *server) Host() string {
	return fmt.Sprintf("%s@%s", s.User, s.IP)
}

func (s *server) Socket() string {
	return fmt.Sprintf("%s:%s", s.Host(), s.Port)
}
