package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

const (
	MIN_TEMP = uint8(0)
	MAX_TEMP = uint8(150)

	MAX_FAN_SPEED_PERCENT = uint8(100)
)

func generateTempNFanSpeedMap(ranges [][2]uint8) map[uint8]uint8 {
	bucket := make(map[uint8]uint8)
	if len(ranges) == 0 {
		// if no ranges, then don't change fan speed
		// by not setting anything in bucket
		return bucket
	}

	// set temp from 0 to the first range to fan OFF
	for i := MIN_TEMP; i < ranges[0][0]; i++ {
		bucket[i] = 0
	}

	// Set fan speed in bucket by given ranges in linear form
	for i, r := range ranges {
		var endRangeTemp uint8
		var endRangeFanSpeed uint8
		if i < len(ranges)-1 {
			endRangeTemp = ranges[i+1][0]
			endRangeFanSpeed = ranges[i+1][1]
		} else {
			endRangeTemp = MAX_TEMP + 1
			endRangeFanSpeed = MAX_FAN_SPEED_PERCENT
		}
		slog.Info("End range", "temp", endRangeTemp, "speed", endRangeFanSpeed)
		slog.Info("Start range", "temp", r[0], "speed", r[1])
		// m = (y_2-y_1)/(x_2-x_1)
		linearSlope := float32(0)
		if endRangeTemp-r[0] != 0 {
			linearSlope = float32(endRangeFanSpeed-r[1]) / float32(endRangeTemp-r[0])
		}
		for temp := r[0]; temp < endRangeTemp; temp++ {
			// y = m(x-x_0)+y_0
			bucket[temp] = uint8(linearSlope*float32(temp-r[0]) + float32(r[1]))
		}
	}

	return bucket
}

func runCustomGPUFanCurve(device nvml.Device, speedMap map[uint8]uint8, pollingDuration time.Duration, dryrun bool, cancel chan bool) error {
	ticker := time.NewTicker(pollingDuration)
	defer ticker.Stop()

	deviceName, ret := device.GetName()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("unable to get device name; err: %s", nvml.ErrorString(ret))
	}
	numFans, ret := nvml.DeviceGetNumFans(device)
	if ret != nvml.SUCCESS {
		return fmt.Errorf("nable to get number of fans from device; err: %s, device: %s", nvml.ErrorString(ret), deviceName)
	}
	for {
		select {
		case <-ticker.C:
			// Get current temperature
			temperature, ret := nvml.DeviceGetTemperature(device, nvml.TEMPERATURE_GPU)
			if ret != nvml.SUCCESS {
				return fmt.Errorf("unable to get device temperature; device: %s, err: %s", deviceName, nvml.ErrorString(ret))
			}
			slog.Debug("current temperature", "temperature", temperature)

			// Get target fan speed based on temperature
			speed, ok := speedMap[uint8(temperature)]
			if !ok {
				slog.Warn("cannot find proper fan speed for given temperature, ignore updating fan speed at this time", "device", deviceName, "temperature", temperature, "buckets", speedMap)
				continue
			}

			// Apply target fan speed to NVIDIA GPU
			for i := 0; i < numFans; i++ {
				if !dryrun {
					slog.Debug("set fan speed", "device", deviceName, "fanIdx", i, "speed", int(speed))
					if ret := nvml.DeviceSetFanSpeed_v2(device, i, int(speed)); ret != nvml.SUCCESS {
						return fmt.Errorf("unable to set fan speed; device: %s, fanIdx: %d, speed: %d, err: %s", deviceName, i, speed, nvml.ErrorString(ret))
					}
				} else {
					slog.Info("(Dryrun) set fan speed", "device", deviceName, "fanIdx", i, "speed", speed)
				}
			}
		case <-cancel:
			return nil
		}
	}
}

func printDeviceInfo(device nvml.Device) {
	uuid, ret := device.GetUUID()
	if ret != nvml.SUCCESS {
		slog.Error("Unable to get uuid of device at index 0", "err", nvml.ErrorString(ret))
		return
	}
	slog.Info("Device UUID", "uuid", uuid)

	deviceName, ret := device.GetName()
	if ret != nvml.SUCCESS {
		slog.Error("Unable to get device name", "err", nvml.ErrorString(ret))
		return
	}
	slog.Info("Device Name", "name", deviceName)

	numFans, ret := nvml.DeviceGetNumFans(device)
	if ret != nvml.SUCCESS {
		slog.Error("Unable to get number of fans from device", "err", nvml.ErrorString(ret), "device", uuid)
		return
	}
	slog.Info("Number of fans", "count", numFans)

	temp, ret := nvml.DeviceGetTemperature(device, nvml.TEMPERATURE_GPU)
	if ret != nvml.SUCCESS {
		slog.Error("Unable to get device temperature", "err", nvml.ErrorString(ret))
		return
	}
	slog.Info("Current temperature", "name", deviceName, "temp", temp)

	tempThreshold, ret := nvml.DeviceGetTemperatureThreshold(device, nvml.TEMPERATURE_THRESHOLD_ACOUSTIC_CURR)
	if ret != nvml.SUCCESS {
		slog.Error("Unable to get temperature threshold", "err", nvml.ErrorString(ret))
		return
	}
	slog.Info("Temperature threshold", "name", deviceName, "temperature", tempThreshold)

	for j := 0; j < numFans; j++ {
		fanSpeed, ret := nvml.DeviceGetFanSpeed_v2(device, j)
		if ret != nvml.SUCCESS {
			slog.Error("Unable to get device fan speed", "err", nvml.ErrorString(ret))
			break
		}
		slog.Info("Fan control speed", "name", deviceName, "fan#", j, "speed", fanSpeed)

		policy, ret := nvml.DeviceGetFanControlPolicy_v2(device, j)
		if ret != nvml.SUCCESS {
			slog.Error("Unable to get fan control policy", "ret", nvml.ErrorString(ret))
			break
		}

		switch policy {
		case nvml.FAN_POLICY_MANUAL:
			slog.Info("Current fan control policy is MANUAL")
		case nvml.FAN_POLICY_TEMPERATURE_CONTINOUS_SW:
			slog.Info("Current fan control policy is TEMPERATURE-BASED automatic")
		default:
			slog.Warn("Unknown fan control policy", "policyID", policy)
		}
	}
}

func parseSpeedConfigFlag(fanSpeedStrConfig string) ([][2]uint8, error) {
	speedPoints := strings.Split(fanSpeedStrConfig, ",")
	var fanSpeedConfig [][2]uint8

	for i, speedPoint := range speedPoints {
		speedPointArr := strings.Split(speedPoint, ":")
		if len(speedPointArr) != 2 {
			return nil, fmt.Errorf("fan speed pair at index %d is not a pair: %s", i, speedPoint)
		}
		temperature, err := strconv.ParseInt(speedPointArr[0], 10, 8)
		if err != nil {
			return nil, fmt.Errorf("unable to parse temperature at pair %d: %w", i, err)
		}
		speed, err := strconv.ParseInt(speedPointArr[1], 10, 8)
		if err != nil {
			return nil, fmt.Errorf("unable to parse fan speed at pair %d: %w", i, err)
		}
		fanSpeedConfig = append(fanSpeedConfig, [2]uint8{uint8(temperature), uint8(speed)})
	}

	return fanSpeedConfig, nil
}

func main() {
	var fanSpeedEncoded string
	var deviceIndex int
	var dryrun bool
	var wg sync.WaitGroup
	var logLevelStr string
	var pollingDuration time.Duration
	cancel := make(chan bool, 1)

	flag.StringVar(&fanSpeedEncoded, "speeds", "35:40,40:50,50:60,60:90,80:100", "Set fan speed linear graph by a list of temperature:fanspeed pair")
	flag.IntVar(&deviceIndex, "device-index", 0, "GPU index to be tuned, if the PC only have 1 GPU, then no need to use this flag")
	flag.BoolVar(&dryrun, "dry-run", false, "Perform dryrun, which won't update any config to the GPU, and show only log to check if config values are correct")
	flag.StringVar(&logLevelStr, "log-level", "INFO", "Adjust log level: DEBUG, INFO, WARN, ERROR")
	flag.DurationVar(&pollingDuration, "polling-duration", 5*time.Second, "Time duration between each polling for fan speed update i.e. 5s, 10s, 1m, etc.")
	flag.Parse()

	fanSpeedConfig, err := parseSpeedConfigFlag(fanSpeedEncoded)
	if err != nil {
		slog.Error("unable to parse fan speed flag", "err", err)
		return
	}

	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(logLevelStr)); err != nil {
		slog.Error("unable to parse log level", "level", logLevelStr, "err", err)
		return
	}
	slog.SetLogLoggerLevel(logLevel)

	speedMap := generateTempNFanSpeedMap(fanSpeedConfig)
	slog.Debug("Fan speed at different temperatures", "temps", speedMap)

	slog.Info("Initialize NVML API")
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		slog.Error("Unable to initialize NVML", "err", nvml.ErrorString(ret))
		return
	}
	defer func() {
		ret := nvml.Shutdown()
		if ret != nvml.SUCCESS {
			slog.Error("Unable to shutdown NVML", "err", nvml.ErrorString(ret))
			return
		}
	}()
	slog.Info("NVML API initialized")

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		slog.Error("Unable to get device count", "err", nvml.ErrorString(ret))
	}
	slog.Info("Found devices", "count", count, "selectedDeviceIdx", deviceIndex)

	device, ret := nvml.DeviceGetHandleByIndex(deviceIndex)
	if ret != nvml.SUCCESS {
		slog.Error("Unable to get device at index", "index", 0, "err", nvml.ErrorString(ret))
		return
	}

	// This function reset NVIDIA GPU fan speed to default policy, before this process exited
	defer func() {
		if dryrun {
			slog.Info("(Dryrun) Set NVIDIA GPU fan speed to default setting", "deviceIdx", deviceIndex)
			return
		}

		numFans, ret := nvml.DeviceGetNumFans(device)
		if ret != nvml.SUCCESS {
			slog.Error("Unable to get number of fans from device", "err", nvml.ErrorString(ret), "deviceIdx", deviceIndex)
		}
		slog.Info("Setting device fan speed policy to default", "deviceIdx", deviceIndex)
		for i := 0; i < numFans; i++ {
			ret := nvml.DeviceSetDefaultFanSpeed_v2(device, i)
			if ret != nvml.SUCCESS {
				slog.Error("Unable to set fan speed to default state", "err", nvml.ErrorString(ret))
			}
		}
	}()

	printDeviceInfo(device)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := runCustomGPUFanCurve(device, speedMap, pollingDuration, dryrun, cancel); err != nil {
			slog.Error("error occurred when run custom GPU fan curve", "err", err)
		}
	}()

	gracefulStop := make(chan os.Signal, 1)
	signal.Notify(gracefulStop, syscall.SIGTERM)
	signal.Notify(gracefulStop, syscall.SIGINT)

	<-gracefulStop
	cancel <- true
	wg.Wait()
	close(cancel)

	slog.Info("Bye, and run deferred functions before exit")
}
