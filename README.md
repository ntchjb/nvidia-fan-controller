# NVIDIA GPU Fan Controller for Linux

A simple NVIDIA fan controller CLI program written in Go, which uses [NVML API](https://github.com/NVIDIA/go-nvml) to communicate with NVIDIA devices.

It should be compatible with any Linux Distros, including both X11 and Wayland.

## Requirement

NVIDIA Driver. I have tested with NVIDIA Driver 555.58.02, but this program should also support any driver version that has NVML API.

## Build

Build requires following tools

- Go 1.22+

Build the program using following command

```sh
cd nvidia-fan-controller
go build -o nvml-fan .
```

The command above generates an executable file named `nvml-fan`, which can be used as CLI program. It can be set to be run in any daemon manager, such as `systemd`. systemd script is shown as an example below.

```
# /etc/systemd/system/nvml-fan.service
[Unit]
Description=NVIDIA Fan controller

[Service]
ExecStart=/path/to/nvml-fan

[Install]
WantedBy=multi-user.target
```

## Usage

```
Usage of ./nvml-fan:
  -device-index int
        GPU index to be tuned, if the PC only have 1 GPU, then no need to use this flag
  -dry-run
        Perform dryrun, which won't update any config to the GPU, and show only log to check if config values are correct
  -log-level string
        Adjust log level: DEBUG, INFO, WARN, ERROR (default "INFO")
  -polling-duration duration
        Time duration between each polling for fan speed update i.e. 5s, 10s, 1m, etc. (default 5s)
  -speeds string
        Set fan speed linear graph by a list of temperature:fanspeed pair (default "35:40,40:50,50:60,60:90,80:100")
```

For fan speed linear graph, each value pair represent temperature and fan speed. The default values can be visualized as follow, where X exis is GPU temperature, and Y axis as fan speed.

The formula is simple, it is multiple linear equations (y=mx+b) pass between 2 given points, which are temperature/speed pairs e.g. from `35:40` to `40:50` pair means temperature from 35 to 40 Celcius, fan speed changes from 40% to 50% of its power.

![](default-fan-speed-graph.png?raw=true)
