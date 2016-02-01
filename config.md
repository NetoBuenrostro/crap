# The Description of the Crap config sample file 

This is how the sample config file looks like. We'll go through this step by step.

```
{
  "crap_version": "1.1",
  "environments": [
    {
      "name": "staging",
      "servers": [
        {
          "port": "22",
          "user": "deployment",
          "ip": "localhost"
        }
      ],
      "restart_command": "(sudo stop myapp_staging || true) \u0026\u0026 sudo start myapp_staging",
      "deploydir": "/var/www/myapp",
      "after_deploy_command": "",
      "default": false
    },
    {
      "name": "production",
      "servers": [
        {
          "port": "22",
          "user": "deployment",
          "ip": "www.myapp.com"
        }
      ],
      "restart_command": "(sudo stop myapp_production || true) \u0026\u0026 sudo start myapp_production",
      "deploydir": "/var/www/myapp",
      "after_deploy_command": "make cleanup",
      "default": false
    }
  ],
  "built_app_dir": "out/myapp",
  "app_build_commands": [
    "make linux64bit"
  ],
  "asset_build_commands": [
    "make css_assets_gzip",
    "make js_assets_gzip"
  ],
  "Campfire": {
    "account": "mycampfireaccount",
    "token": "foobarfoobarfoobar",
    "rooms": "8343,234223"
  }
}
```

- `crap_version` - Version number of crap that created the file is stored in this parameter

- **environments**
(_Array of environments defined_)
  - `name` - name of the environment (this is the same name that should be used when executing crap)
  - **`servers`**
(_Array of servers that will be deployed to_)
    - `port` - Port of the server connection
    - `user` - User name that is used to connect to the server
    - `ip` - IP address of the server
  - `restart_command` - Command that is executed on the server after deploy to restart the service
  - `deploydir` - Location of the project on the server
  - `after_deploy_command` - Additional deploy command to clean up or something else that is executed after deploy
  - `default` - If default is set to true the environment is accounted as default and will be used when `crap` is executed.

- `build_app_dir` - Location where all the files that need to be uploaded are copied
- `app_build_commands` - Array of commands that are ran locally to compile the app
- `asset_build_commands` - Array of commands that package and minify the assets

- **`Campfire`**
(_Sends notification message to campfire after deploy_)
  - `account` - Campfire account username
  - `token` - Campfire account token
  - `rooms` - Comma separated list of Campfire rooms that are notfied after deploy

