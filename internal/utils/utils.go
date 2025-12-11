package utils

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	saladclient "github.com/SaladTechnologies/salad-client"
	corev1 "k8s.io/api/core/v1"
)

// roundUpToNearest returns the nearest larger integer from the given list.
func roundUpToNearest(value int64, list []int64) int64 {
	for _, v := range list {
		if value <= v {
			return v
		}
	}
	return list[len(list)-1]
}

// GetPodResource returns the total CPU in rounded cores and memory rounded to gibibytes (GiB) for the provided PodSpec.
func GetPodResource(podSpec corev1.PodSpec) (cpu int64, memory int64) {
	allowedCPUValues := []int64{1, 2, 3, 4, 6, 8, 12, 16}

	allowedMemoryValues := []int64{1024, 2048, 3072, 4 * 1024, 6 * 1024, 8 * 1024, 12 * 1024, 16 * 1024, 24 * 1024, 30 * 1024, 38 * 1024, 60 * 1024} // in GiB

	for _, container := range podSpec.Containers {
		// Convert milliCPU to cores and round to nearest value in the list
		cpuValue := container.Resources.Requests.Cpu().MilliValue() / 1000
		cpu += roundUpToNearest(cpuValue, allowedCPUValues)

		// Convert bytes to gibibytes (GiB) and ensure it's a multiple of 1 GiB (1 GiB = 1024*1024 bytes)
		memValue := container.Resources.Requests.Memory().Value() / (1024 * 1024)
		memory += roundUpToNearest(memValue, allowedMemoryValues)
	}
	return
}

func GetPodName(nameSpace, containerGroup string, pod *corev1.Pod) string {
	if nameSpace == "" {
		nameSpace = pod.Namespace
	}
	if containerGroup == "" {
		containerGroup = pod.Name
	}
	if nameSpace == "" && containerGroup == "" && pod.Spec.Containers[0].Name != "" {
		return pod.Spec.Containers[0].Name
	}
	return nameSpace + "-" + containerGroup
}

func GetPodPhaseFromContainerGroupState(containerGroupState saladclient.ContainerGroupState) corev1.PodPhase {
	switch containerGroupState.Status {
	case saladclient.CONTAINERGROUPSTATUS_PENDING:
		return corev1.PodPending
	case saladclient.CONTAINERGROUPSTATUS_RUNNING:
		{
			if containerGroupState.InstanceStatusCounts.RunningCount > 0 {
				return corev1.PodRunning
			}
			return corev1.PodPending
		}
	case saladclient.CONTAINERGROUPSTATUS_FAILED:
		return corev1.PodFailed
	case saladclient.CONTAINERGROUPSTATUS_SUCCEEDED:
		return corev1.PodSucceeded
	case saladclient.CONTAINERGROUPSTATUS_STOPPED:
		return corev1.PodSucceeded
	case saladclient.CONTAINERGROUPSTATUS_DEPLOYING:
		return corev1.PodPending

	}

	return ""

}

// GetProblemDetailsFromError extracts ProblemDetails from a salad-client GenericOpenAPIError.
// The salad-client already parses the response body into a ProblemDetails model,
// so we should use that instead of trying to re-read the response body.
func GetProblemDetailsFromError(err error) *saladclient.ProblemDetails {
	if err == nil {
		return nil
	}

	// Try to get the GenericOpenAPIError which contains the parsed model
	type modelGetter interface {
		Model() interface{}
		Body() []byte
	}

	if apiErr, ok := err.(modelGetter); ok {
		// The salad-client stores the parsed ProblemDetails in the Model field
		if model := apiErr.Model(); model != nil {
			if pd, ok := model.(saladclient.ProblemDetails); ok {
				return &pd
			}
		}

		// If Model() didn't have ProblemDetails, try to parse the body
		if body := apiErr.Body(); len(body) > 0 {
			var pd saladclient.ProblemDetails
			if jsonErr := json.Unmarshal(body, &pd); jsonErr == nil {
				return &pd
			}
			// Couldn't parse, create a synthetic error with the raw body
			syntheticPD := saladclient.NewProblemDetails()
			syntheticPD.SetType("parse_error")
			syntheticPD.SetTitle("Failed to parse error response")
			syntheticPD.SetDetail(string(body))
			return syntheticPD
		}
	}

	// Fallback: create a ProblemDetails from the error message
	syntheticPD := saladclient.NewProblemDetails()
	syntheticPD.SetType("unknown_error")
	syntheticPD.SetTitle("API Error")
	syntheticPD.SetDetail(err.Error())
	return syntheticPD
}

// GetResponseBody reads and parses the response body as ProblemDetails.
// Deprecated: Use GetProblemDetailsFromError instead, as the salad-client
// already parses the response body before returning.
func GetResponseBody(response *http.Response) (*saladclient.ProblemDetails, error) {
	if response == nil {
		return nil, fmt.Errorf("response was nil")
	}

	// Get response body for error info
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, fmt.Errorf("failed to read response body: %w", readErr)
	}
	closeErr := response.Body.Close()
	if closeErr != nil {
		return nil, closeErr
	}
	response.Body = io.NopCloser(bytes.NewBuffer(body))

	// Get a ProblemDetails struct from the body
	pd := saladclient.NewNullableProblemDetails(nil)
	err := pd.UnmarshalJSON(body)
	if err != nil {
		// Can't decode ProblemDetails, just make one and return the string
		npd := saladclient.NewProblemDetails()
		npd.SetType("unknown_error")
		npd.SetTitle("Error decoding response body")
		npd.SetDetail(string(body))
		pd.Set(npd)
	} else {
		// Check if the decoded ProblemDetails has empty fields
		// This can happen when the API returns an empty JSON object or unexpected format
		result := pd.Get()
		if result != nil && result.Type == nil && result.Title == nil && result.Detail == nil {
			// All fields are nil, include the raw body for debugging
			npd := saladclient.NewProblemDetails()
			npd.SetType("empty_error_response")
			npd.SetTitle(fmt.Sprintf("HTTP %d", response.StatusCode))
			if len(body) > 0 {
				npd.SetDetail(string(body))
			} else {
				npd.SetDetail("(empty response body)")
			}
			pd.Set(npd)
		}
	}
	return pd.Get(), nil
}
