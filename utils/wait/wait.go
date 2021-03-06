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

package wait

import (
	"errors"
	"time"

	log "github.com/sirupsen/logrus"

	"os/exec"

	"github.com/paypal/dce-go/types"
)

type ConditionCHFunc func(done chan string)
type ConditionFunc func() (string, error)

var ErrTimeOut = errors.New("timed out waiting for the condition")

// Keep polling a condition func until it return a message or an error
// PollForever always wait interval
// PollForever will keep polling forever, no timeout
func PollForever(interval time.Duration, done <-chan string, condition ConditionFunc) (string, error) {
	return PollUntil(interval, done, 0, condition)
}

// PollRetry repeats running condition functions with a backoff until it runs successfully
// Or it already retried multiple times which is set in config
// The backoff time will be retry * interval
func PollRetry(retry int, interval time.Duration, condition ConditionFunc) error {
	log.Println("PullRetry : max pull retry is set as", retry)
	//log.Println("PullRetry : timeout :", timeout)
	log.Println("PullRetry : interval:", interval)
	var err error
	for i := 0; i < retry; i++ {
		if i != 0 {
			log.Println("Condition Func failed, Start Retrying : ", i)
		}
		//timeout, _, err = CountDown(timeout, condition)
		_, err = condition()
		if err == nil {
			return nil
		}

		time.Sleep(time.Duration(i+1) * interval)
		//timeout -= time.Duration(i+1) * interval
	}
	return ErrTimeOut
}

// Keep polling a condition func until timeout or a message/error is returned
func PollUntil(interval time.Duration, done <-chan string, timeout time.Duration, condition ConditionFunc) (string, error) {
	tricker := time.NewTicker(interval)
	defer tricker.Stop()

	var after <-chan time.Time
	if timeout != 0 {
		timer := time.NewTimer(timeout)
		after = timer.C
		defer timer.Stop()
	}

	for {
		select {
		case <-tricker.C:
			res, err := WaitUntil(timeout, ConditionCHFunc(func(reply chan string) {
				condition_reply, _ := condition()
				reply <- condition_reply
			}))
			if err != nil {
				return res, err
			}
			if res != "" {
				return res, nil
			}
		case <-after:
			return "", ErrTimeOut
		case mesg := <-done:
			return mesg, nil
		}
	}
}

// Run condition in goroutine, wait for condition's return until timeout
// If timeout, return ErrTimeOut
// If message received from condition, return message
func WaitUntil(timeout time.Duration, condition ConditionCHFunc) (string, error) {
	var after <-chan time.Time
	if timeout != 0 {
		timer := time.NewTimer(timeout)
		after = timer.C
		defer timer.Stop()
	}

	replyCH := make(chan string, 1)
	go condition(replyCH)

	select {
	case <-after:
		return "", ErrTimeOut
	case res := <-replyCH:
		return res, nil
	}
}

// count down the time left after condition func is finished
func CountDown(timeout time.Duration, condition ConditionFunc) (time.Duration, string, error) {
	if timeout > 0 {
		var start time.Time
		start = time.Now()
		res, err := condition()
		return timeout - time.Since(start), res, err
	}
	return 0, "", ErrTimeOut
}

// wait on exec command finished or timeout
func WaitCmd(timeout time.Duration, cmd_result *types.CmdResult) error {
	log.Println("test:::::", timeout)
	if timeout < time.Duration(0) {
		log.Println("TIMEOUT is less than zero")
		return ErrTimeOut
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd_result.Command.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			log.Println("cmd.wait() return error :", err.Error())
		}

		return err

	case <-time.After(timeout):
		log.Println("cmd.wait() timeout :", ErrTimeOut.Error())
		return ErrTimeOut
	}
}

// Retry command util reach the maximum try out count
func RetryCmd(retry int, cmd *exec.Cmd) ([]byte, error) {
	var err error
	var out []byte
	for i := 0; i < retry; i++ {
		cmd := exec.Command(cmd.Args[0], cmd.Args[1:]...)
		out, err = cmd.Output()
		if err != nil {
			log.Warnf("Error to exec cmd %v with count %d : %v", cmd.Args, i, err)
			continue
		}
		return out, nil
	}
	return nil, err
}
