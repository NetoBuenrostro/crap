# Crap
====

A deployment tool similar to Capistrano, but simplerer

## Install
Clone the repo, then in the root of crap project run install command.

```
make install
```

Command builds the go binary and copies it to /usr/bin

## How to use
- Go to your project root and run 'crap --crapify'
- This generates sample crap configuration file called `crap.json` (see this file for config file details [config.md](config.md) )
- Edit the config file to match your configuration
- Run `crap staging` to deploy to staging
- Run `crap production` to deploy to staging

### Command line arguments
```
--crapify   Generates sample crap config file
--verbose   Enable verbose logging
--version   Print Crap version and exit

## Uninstall

In the root of crap project run install command.

```
make uninstall
```