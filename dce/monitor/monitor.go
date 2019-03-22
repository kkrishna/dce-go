/*
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

//Monitor pod status
package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	dockertype "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"time"

	"github.com/paypal/dce-go/config"
	"github.com/paypal/dce-go/types"
	"github.com/paypal/dce-go/utils"
	"github.com/paypal/dce-go/utils/pod"
	log "github.com/sirupsen/logrus"
)

func podMonitorEvent(systemProxyId string) types.PodStatus {
	logger := log.WithFields(log.Fields{
		"func": "monitor.podMonitorEvent",
	})


	isService := config.IsService()
	logger.Printf("Task is SERVICE: %v", isService)
	logger.Println(" Container Monitor List: ", pod.MonitorContainerList)

	ctx := context.Background()
	cli, err := client.NewEnvClient()
	if err != nil {
		logger.Errorln("Docker Client error: ", err.Error())
		return types.POD_FAILED
	}

	log.Println("Negotiating API version ..")
	cli.NegotiateAPIVersion(ctx)
	log.Println("adding new listener")
	filters := filters.NewArgs()
	filters.Add("type", events.ContainerEventType)

	for _, cid := range pod.MonitorContainerList {
		filters.Add("container", cid)
	}
	filters.Add("event", "die")
	filters.Add("event", "destroy")
	filters.Add("event", "kill")
	filters.Add("event", "stop")
	filters.Add("event", "health_status")
	filters.Add("event", "oom")
	filters.Add("event", "restart")
	filters.Add("event", "start")
	filters.Add("event", "pause")
	filters.Add("event", "unpause")

	options := dockertype.EventsOptions{Filters: filters}
	eventsChan, errChan := cli.Events(ctx, options)

	for {
		select {
		case err := <-errChan:
			if err != nil {
				log.Println("error in event channel-- ", err)
				//TODO: check container list in pod after docker client reconnect!
				log.Println(" Sleeping for 10 sec ", time.Now())
				time.Sleep(10 * time.Second)
				eventsChan, errChan = cli.Events(context.Background(), options)
			}

		case event := <-eventsChan:
			if bytes, err := json.Marshal(event); err != nil {
				log.Fatalf("error Marshalling: %v", err)
			} else {
				log.Println(" Message JSON: ", string(bytes))
			}

			containerId := event.Actor.ID
			logger.Println("Message Payload: ", event)

			if event.Type == "container" {
				if event.Action == "health_status: healthy" {
					logger.Println("healthy event received for container -- ", containerId)
				} else if event.Action == "health_status: unhealthy" {
					logger.Println("unhealthy event received for container -- ", containerId)
					ctx.Done()
					return types.POD_FAILED
				} else if event.Action == "stop" || event.Action == "die" {
					logger.Println(event.Action, "  -- stop/die event received for container %s", containerId)
					ctx := context.Background()
					if details, err := cli.ContainerInspect(ctx, event.Actor.ID); err != nil {
						logger.Println("error: ", err.Error())
					} else {
						logger.Println(" container inspect: ", details)
						exitCode := details.State.ExitCode
						isRunning := details.State.Running
						logger.Println(" exitCode: ", exitCode, "   isRunning: ", isRunning)
						if exitCode == 0 && !isRunning {
							//Remove container from the list !!
							for i := 0; i < len(pod.MonitorContainerList); i++ {
								if pod.MonitorContainerList[i] == event.Actor.ID {
									pod.MonitorContainerList = append(pod.MonitorContainerList[:i], pod.MonitorContainerList[i+1:]...)
									logger.Println("containerList: ", pod.MonitorContainerList)
									if len(pod.MonitorContainerList) == 0 && !isService {
										logger.Println("Task is ADHOC job. All containers in the pod exit with code 0, sending FINISHED")
										return types.POD_FINISHED
									}
									if len(pod.MonitorContainerList) == 0 {
										logger.Println("Task is SERVICE. All containers in the pod exit with code 0, sending FAILED")
										return types.POD_FAILED
									}
									if len(pod.MonitorContainerList) == 1 && pod.MonitorContainerList[0] == systemProxyId && !isService {
										logger.Println("Task is ADHOC job. Only infra container is running in the pod, sending FINISHED")
										return types.POD_FINISHED
									}
									if len(pod.MonitorContainerList) == 1 && pod.MonitorContainerList[0] == systemProxyId {
										logger.Println("Task is SERVICE. Only infra container is running in the pod, sending FAILED")
										return types.POD_FAILED
									}
									break
								}
							}
						} else if exitCode != 0 {
							return types.POD_FAILED
						}
					}
				} else if event.Action == "start" {
					logger.Println(" start event received for container -- ", containerId)
				}
			}
		}
	}
	return types.POD_EMPTY
}


// Watching pod status and notifying executor if any container in the pod goes wrong
func podMonitor(systemProxyId string) types.PodStatus {
	logger := log.WithFields(log.Fields{
		"func": "monitor.podMonitor",
	})

	var err error

	for i := 0; i < len(pod.MonitorContainerList); i++ {
		var healthy types.HealthStatus
		var exitCode int
		var running bool

		if hc, ok := pod.HealthCheckListId[pod.MonitorContainerList[i]]; ok && hc {
			healthy, running, exitCode, err = pod.CheckContainer(pod.MonitorContainerList[i], true)
			logger.Debugf("container %s has health check, health status: %s, exitCode: %d, err : %v",
				pod.MonitorContainerList[i], healthy.String(), exitCode, err)
		} else {
			healthy, running, exitCode, err = pod.CheckContainer(pod.MonitorContainerList[i], false)
			log.Debugf("container %s doesn't have health check, status: %s, exitCode: %d, err : %v",
				pod.MonitorContainerList[i], healthy.String(), exitCode, err)
		}

		if err != nil {
			logger.Errorf(fmt.Sprintf("POD_MONITOR_HEALTH_CHECK_FAILED -- Error inspecting container with id : %s, %v", pod.MonitorContainerList[i], err.Error()))
			logger.Errorln("POD_MONITOR_FAILED -- Send Failed")
			return types.POD_FAILED
		}

		if exitCode != 0 {
			logger.Println("POD_MONITOR_APP_EXIT -- Stop pod monitor and send Failed")
			return types.POD_FAILED
		}

		if healthy == types.UNHEALTHY {
			if config.GetConfigSection(config.CLEANPOD) == nil ||
				config.GetConfigSection(config.CLEANPOD)[types.UNHEALTHY.String()] == "true" {
				logger.Println("POD_MONITOR_HEALTH_CHECK_FAILED -- Stop pod monitor and send Failed")
				return types.POD_FAILED
			}
			logger.Warnf("Container %s became unhealthy, but pod won't be killed due to cleanpod config", pod.MonitorContainerList[i])
		}

		if exitCode == 0 && !running {
			logger.Printf("Removed finished(exit with 0) container %s from monitor list", pod.MonitorContainerList[i])
			pod.MonitorContainerList = append(pod.MonitorContainerList[:i], pod.MonitorContainerList[i+1:]...)
			i--

		}
	}

	// Send finished to mesos IF no container running or ONLY system proxy is running in the pod
	isService := config.IsService()
	if len(pod.MonitorContainerList) == 0 && !isService {
		logger.Println("Task is ADHOC job. All containers in the pod exit with code 0, sending FINISHED")
		return types.POD_FINISHED
	}

	if len(pod.MonitorContainerList) == 0 {
		logger.Println("Task is SERVICE. All containers in the pod exit with code 0, sending FAILED")
		return types.POD_FAILED
	}

	if len(pod.MonitorContainerList) == 1 && pod.MonitorContainerList[0] == systemProxyId && !isService {
		logger.Println("Task is ADHOC job. Only infra container is running in the pod, sending FINISHED")
		return types.POD_FINISHED
	}

	if len(pod.MonitorContainerList) == 1 && pod.MonitorContainerList[0] == systemProxyId {
		logger.Println("Task is SERVICE. Only infra container is running in the pod, sending FAILED")
		return types.POD_FAILED
	}

	return types.POD_EMPTY
}

// Polling pod monitor periodically
func MonitorPoller() {
	logger := log.WithFields(log.Fields{
		"func": "monitor.MonitorPoller",
	})

	logger.Println("====================Pod Monitor Poller====================")

	// Get infra container id
	var infraContainerId string
	var err error
	if !config.GetConfig().GetBool(types.RM_INFRA_CONTAINER) {
		infraContainerId, err = pod.GetContainerIdByService(pod.ComposeFiles, types.INFRA_CONTAINER)
		if err != nil {
			logger.Errorf("Error getting container id of service %s: %v", types.INFRA_CONTAINER, err)
			logger.Errorln("POD_MONITOR_FAILED -- Send Failed")
			pod.SendPodStatus(types.POD_FAILED)
			return
		}
		logger.Printf("Infra container id: %s", infraContainerId)
	}

	//res, err := wait.PollForever(time.Duration(config.GetPollInterval())*time.Millisecond, nil, wait.ConditionFunc(func() (string, error) {
	//	return podMonitor(infraContainerId).String(), nil
	//}))

	res := podMonitorEvent(infraContainerId).String()

	logger.Printf("Pod Monitor Receiver : Received  message %s", res)

	curPodStatus := pod.GetPodStatus()
	if curPodStatus == types.POD_KILLED || curPodStatus == types.POD_FAILED {
		logger.Println("====================Pod Monitor Stopped====================")
		return
	}

	if err != nil {
		pod.SendPodStatus(types.POD_FAILED)
		return
	}

	switch utils.ToPodStatus(res) {
	case types.POD_FAILED:
		pod.SendPodStatus(types.POD_FAILED)

	case types.POD_FINISHED:
		pod.SendPodStatus(types.POD_FINISHED)
	}
}
