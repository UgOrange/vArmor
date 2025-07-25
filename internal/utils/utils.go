// Copyright 2022-2023 vArmor Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package utils implements the utils for vArmor.
package utils

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/version"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"

	varmor "github.com/bytedance/vArmor/apis/varmor/v1beta1"
	varmorinterface "github.com/bytedance/vArmor/pkg/client/clientset/versioned/typed/varmor/v1beta1"
)

const (
	httpTimeout = 3 * time.Second
	retryTimes  = 5
)

func HTTPSPostWithRetryAndToken(address string, path string, reqBody []byte, inContainer bool) error {
	url := fmt.Sprintf("https://%s%s", address, path)

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Timeout: httpTimeout, Transport: tr}

	var lastErr error
	for range retryTimes {
		httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
		if err == nil {
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Token", GetToken())

			httpRsp, err := client.Do(httpReq)
			if err == nil {
				switch httpRsp.StatusCode {
				case http.StatusOK:
					httpRsp.Body.Close()
					return nil
				case http.StatusUnauthorized:
					if inContainer {
						// try update token
						updateChan <- true
					}
				default:
					lastErr = fmt.Errorf("http error code %d", httpRsp.StatusCode)
				}
				httpRsp.Body.Close()
			} else {
				lastErr = err
			}
		} else {
			lastErr = err
		}

		time.Sleep(time.Duration(rand.Intn(500)+200) * time.Millisecond)
	}

	return lastErr
}

func HTTPPostAndGetResponseWithRetry(address string, path string, reqBody []byte) ([]byte, error) {
	url := fmt.Sprintf("http://%s%s", address, path)

	client := &http.Client{Timeout: httpTimeout}

	var lastErr error
	for range retryTimes {
		httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
		if err == nil {
			httpReq.Header.Set("Content-Type", "application/json")

			httpRsp, err := client.Do(httpReq)
			if err == nil {
				if httpRsp.StatusCode == http.StatusOK {
					rspBody := make([]byte, len(reqBody))
					var n int
					n, err = httpRsp.Body.Read(rspBody)
					if n > 0 && err == io.EOF {
						httpRsp.Body.Close()
						return rspBody, nil
					}
				} else {
					lastErr = fmt.Errorf("http error code %d", httpRsp.StatusCode)
				}
				httpRsp.Body.Close()
			} else {
				lastErr = err
			}
		} else {
			lastErr = err
		}

		time.Sleep(time.Duration(rand.Intn(500)+200) * time.Millisecond)
	}

	return nil, lastErr
}

func TagLeaderPod(podInterface corev1.PodInterface, name string) error {
	jsonPatch := `[{"op": "add", "path": "/metadata/labels/identity", "value": "leader"}]`
	_, err := podInterface.Patch(context.Background(), name, types.JSONPatchType, []byte(jsonPatch), metav1.PatchOptions{})

	return err
}

func UnTagLeaderPod(podInterface corev1.PodInterface) error {
	matchLabels := map[string]string{
		"app.kubernetes.io/component": "varmor-manager",
		"identity":                    "leader",
	}

	listOpt := metav1.ListOptions{
		LabelSelector:   labels.Set(matchLabels).String(),
		ResourceVersion: "0",
	}
	pods, err := podInterface.List(context.Background(), listOpt)
	if err != nil {
		if k8errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	for _, pod := range pods.Items {
		jsonPatch := `[{"op": "remove", "path": "/metadata/labels/identity"}]`
		_, err := podInterface.Patch(context.Background(), pod.Name, types.JSONPatchType, []byte(jsonPatch), metav1.PatchOptions{})
		if err != nil {
			return err
		}
	}

	return err
}

func InStringArray(c string, array []string) bool {
	for _, v := range array {
		if v == c {
			return true
		}
	}
	return false
}

func InUint32Array(i uint32, array []uint32) bool {
	for _, v := range array {
		if v == i {
			return true
		}
	}
	return false
}

func InUint16Array(i uint16, array []uint16) bool {
	for _, v := range array {
		if v == i {
			return true
		}
	}
	return false
}

func InPortRangeArray(i varmor.Port, array []varmor.Port) bool {
	for _, v := range array {
		if v.Port == i.Port && v.EndPort == i.EndPort {
			return true
		}
	}
	return false
}

func InNetworksArray(i varmor.NetworkContent, array []varmor.NetworkContent) bool {
	for _, v := range array {
		if reflect.DeepEqual(v, i) {
			return true
		}
	}
	return false
}

func GinLogger() gin.HandlerFunc {
	return gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		o := fmt.Sprintf("%s [GIN] \"statusCode\"=\"%d\" \"latency\"=\"%v\" \"clientIP\"=\"%s\" \"method\"=\"%s\" \"path\"=\"%s\" \"msg\"=\"%s\"\n",
			time.Now().Format("0102 15:04:05.000000"),
			param.StatusCode,
			param.Latency,
			param.ClientIP,
			param.Method,
			param.Path,
			param.ErrorMessage)

		if param.StatusCode&200 != 200 {
			return "E" + o
		}
		return ""
	})
}

func IsAppArmorGA(versionInfo *version.Info) (bool, error) {
	major, err := strconv.Atoi(versionInfo.Major)
	if err != nil {
		return false, err
	}

	minor, err := strconv.Atoi(strings.TrimRight(versionInfo.Minor, "+"))
	if err != nil {
		return false, err
	}

	if major <= 1 && minor < 30 {
		return false, nil
	}
	return true, nil
}

func RemoveArmorProfileFinalizers(i varmorinterface.CrdV1beta1Interface, namespace, name string) error {
	removeFinalizers := func() error {
		ap, err := i.ArmorProfiles(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			if k8errors.IsNotFound(err) {
				return nil
			}
			return err
		}
		ap.Finalizers = []string{}
		_, err = i.ArmorProfiles(namespace).Update(context.Background(), ap, metav1.UpdateOptions{})
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, removeFinalizers)
}

func IsRequestSizeError(err error) bool {
	errMsg := err.Error()
	if strings.Contains(errMsg, "trying to send message larger than max") ||
		strings.Contains(errMsg, "etcdserver: request is too large") ||
		strings.Contains(errMsg, "Request entity too large") {
		return true
	}
	return false
}
