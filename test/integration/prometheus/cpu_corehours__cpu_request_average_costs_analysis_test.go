package prometheus

// Description: Checks all CPU related Costs
// - CPUCores
// - CPUCoresHours
// - CPUCoresRequestAverage
// - CPUMaxUsage

// Testing Methodology
// 1. Query CPUAllocated and CPURequested in prometheus
//     1.1 Use the current "time" as upperbound in promql
//     1.2 Query by (container, pod, node, namespace)
// 2. Consolidate containers based on pods
//     2.1 CPUCore is the max of CPUCoreAllocated and CPUCoreRequested for each container
//     2.2 Query [24h:5m] to get 288 (1440/5) points to identify the StartTime and EndTime DataPoint Timestamp.
// 	   2.3 Assumption 1: Identify the time range for the pod (all containers within the pod have the same time range)
// 3. Consolidate CPUCores based on pod and then based on namespace
// 4. Fetch /allocation data aggregated by namespace
// 5. Compare results with a 5% error margin.

import (
	"testing"
	"time"

	"github.com/opencost/opencost-integration-tests/pkg/api"
	"github.com/opencost/opencost-integration-tests/pkg/prometheus"
	"github.com/opencost/opencost-integration-tests/pkg/utils"
)

// 10 Minutes
const cpuCorevsCpuRequestShortLivedPodsRunTime = 60
const cpuCorevsCpuRequestResolution = "1m"
const cpuCorevsCpuRequestTolerance = 0.07
const cpuCorevsCpuRequestnegligibleCores = 0.01

func TestCPUCosts(t *testing.T) {
	apiObj := api.NewAPI()

	// test for more windows
	testCases := []struct {
		name       string
		window     string
		aggregate  string
		accumulate string
	}{
		{
			name:       "Yesterday",
			window:     "24h",
			aggregate:  "namespace",
			accumulate: "false",
		},
		{
			name:       "Last Two Days",
			window:     "48h",
			aggregate:  "namespace",
			accumulate: "false",
		},
	}

	t.Logf("testCases: %v", testCases)

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			// ----------------------------------------------
			// Allocation API Data Collection
			// ----------------------------------------------
			// /compute/allocation: CPU Costs for all namespaces
			apiResponse, err := apiObj.GetAllocation(api.AllocationRequest{
				Window:     tc.window,
				Aggregate:  tc.aggregate,
				Accumulate: tc.accumulate,
			})

			if err != nil {
				t.Fatalf("Error while calling Allocation API %v", err)
			}
			if apiResponse.Code != 200 {
				t.Errorf("API returned non-200 code")
			}

			// ----------------------------------------------
			// Prometheus Data Collection
			// ----------------------------------------------
			client := prometheus.NewClient()

			// Loop over namespaces
			for namespace, allocationResponseItem := range apiResponse.Data[0] {

				// Use this information to find start and end time of pod
				queryEnd := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
				// Get Time Duration
				timeNumericVal, _ := utils.ExtractNumericPrefix(tc.window)
				// Assume the minumum unit is an hour
				negativeDuration := time.Duration(timeNumericVal*float64(time.Hour)) * -1
				queryStart := queryEnd.Add(negativeDuration)
				window24h := api.Window{
					Start: queryStart,
					End:   queryEnd,
				}
				// Note that in the Pod Query, we use a 5m resolution [THIS IS THE DEFAULT VALUE IN OPENCOST]
				resolutionNumericVal, _ := utils.ExtractNumericPrefix(cpuCorevsCpuRequestResolution)
				resolution := time.Duration(int(resolutionNumericVal) * int(time.Minute))

				// Query End Time for all Queries
				endTime := queryEnd.Unix()

				windowRange := prometheus.GetOffsetAdjustedQueryWindow(tc.window, cpuCorevsCpuRequestResolution)
				// Metric: CPURequests
				// avg(avg_over_time(
				// 		kube_pod_container_resource_requests{
				// 			resource="cpu", unit="core", container!="", container!="POD", node!=""
				// 		}[24h])
				// )
				// by
				// (container, pod, namespace, node)

				// Q) What about Cluster Filter and Cluster Label?
				promCPURequestedInput := prometheus.PrometheusInput{}

				promCPURequestedInput.Metric = "kube_pod_container_resource_requests"
				promCPURequestedInput.Filters = map[string]string{
					// "job": "opencost", Averaging all results negates this process
					"resource":  "cpu",
					"unit":      "core",
					"namespace": namespace,
				}
				promCPURequestedInput.IgnoreFilters = map[string][]string{
					"container": {"", "POD"},
					"node":      {""},
				}
				promCPURequestedInput.AggregateBy = []string{"container", "pod", "namespace", "node", "uid"}
				promCPURequestedInput.Function = []string{"avg_over_time", "avg"}
				promCPURequestedInput.QueryWindow = windowRange
				promCPURequestedInput.Time = &endTime

				requestedCPU, err := client.RunPromQLQuery(promCPURequestedInput)
				if err != nil {
					t.Fatalf("Error while calling Prometheus API %v", err)
				}

				// Metric: CPUAllocated
				// avg(avg_over_time(
				// 		container_cpu_allocation{
				// 			container!="", container!="POD", node!=""
				// 		}[24h])
				// )
				// by
				// (container, pod, namespace, node)

				// Q) What about Cluster Filter and Cluster Label?
				promCPUAllocatedInput := prometheus.PrometheusInput{}

				promCPUAllocatedInput.Metric = "container_cpu_allocation"
				promCPUAllocatedInput.Filters = map[string]string{
					// "job": "opencost", Averaging all results negates this process
					"namespace": namespace,
				}
				promCPUAllocatedInput.IgnoreFilters = map[string][]string{
					"container": {"", "POD"},
					"node":      {""},
				}
				promCPUAllocatedInput.AggregateBy = []string{"container", "pod", "namespace", "node", "uid"}
				promCPUAllocatedInput.Function = []string{"avg_over_time", "avg"}
				promCPUAllocatedInput.QueryWindow = windowRange
				promCPUAllocatedInput.Time = &endTime

				allocatedCPU, err := client.RunPromQLQuery(promCPUAllocatedInput)
				if err != nil {
					t.Fatalf("Error while calling Prometheus API %v", err)
				}

				// Query all running pod information
				// avg(kube_pod_container_status_running{} != 0)
				// by
				// (container, pod, namespace, node)[24h:5m]

				// Q) != 0 is not necessary I suppose?
				promPodInfoInput := prometheus.PrometheusInput{}

				promPodInfoInput.Metric = "kube_pod_container_status_running"
				promPodInfoInput.Filters = map[string]string{
					"namespace": namespace,
				}
				promPodInfoInput.MetricNotEqualTo = "0"
				promPodInfoInput.AggregateBy = []string{"container", "pod", "namespace", "node", "uid"}
				promPodInfoInput.Function = []string{"avg"}
				promPodInfoInput.AggregateWindow = windowRange
				promPodInfoInput.AggregateResolution = cpuCorevsCpuRequestResolution
				promPodInfoInput.Time = &endTime

				podInfo, err := client.RunPromQLQuery(promPodInfoInput)
				if err != nil {
					t.Fatalf("Error while calling Prometheus API %v", err)
				}

				// Define Local Struct for PoD Data Consolidation
				// PoD is composed of multiple containers, we want to represent all that information succinctly here
				type ContainerCPUData struct {
					Container              string
					CPUCoresRequestAverage float64
					CPUCores               float64
					CPUCoresHours          float64
				}

				type PodData struct {
					Pod        string
					Namespace  string
					Start      time.Time
					End        time.Time
					Minutes    float64
					Containers map[string]*ContainerCPUData
				}

				// ----------------------------------------------
				// Identify All Pods and Containers for that Pod

				// Create a map of PodData for each pod, and calculate the runtime.
				// The query we make for pods is _NOT_ an average over time "range" vector. Instead, it's a range vector
				// that returns _ALL_ samples for the pod over the last 24 hours. So, when you see <metric>[24h:5m] the
				// number after the ':' (5m) is the resolution of the samples. So for the pod query, we get 24h / 5m
				// ----------------------------------------------

				// Pointers to modify in place
				podMap := make(map[string]*PodData)

				for _, podInfoResponseItem := range podInfo.Data.Result {
					// The summary of this method is that we grab the _last_ sample time and _first_ sample time
					// from the pod's metrics, which roughly represents the start and end of the pod's lifetime
					// WITHIN the query window (24h in this case).
					s, e := prometheus.CalculateStartAndEnd(podInfoResponseItem.Values, resolution, window24h)

					// Add a key in the podMap for current Pod
					podMap[podInfoResponseItem.Metric.Pod] = &PodData{
						Pod:        podInfoResponseItem.Metric.Pod,
						Namespace:  namespace,
						Start:      s,
						End:        e,
						Minutes:    e.Sub(s).Minutes(),
						Containers: make(map[string]*ContainerCPUData),
					}
				}
				// ----------------------------------------------
				// Gather CPUCores (CPUAllocated) for every container in a Pod

				// Iterate over results and group by pod
				// ----------------------------------------------

				for _, CPUAllocatedItem := range allocatedCPU.Data.Result {
					container := CPUAllocatedItem.Metric.Container
					pod := CPUAllocatedItem.Metric.Pod
					if container == "" {
						t.Logf("Skipping CPU allocation for empty container in pod %s in namespace: %s", pod, CPUAllocatedItem.Metric.Namespace)
						continue
					}
					podData, ok := podMap[pod]
					if !ok {
						t.Logf("Failed to find namespace: %s and pod: %s in CPU allocated results", CPUAllocatedItem.Metric.Namespace, pod)
						continue
					}
					CPUCores := CPUAllocatedItem.Value.Value
					runMinutes := podData.Minutes
					if runMinutes <= 0 {
						t.Logf("Namespace: %s, Pod %s has a run duration of 0 minutes, skipping CPU allocation calculation", podData.Namespace, podData.Pod)
						continue
					}

					runHours := utils.ConvertToHours(runMinutes)
					podData.Containers[container] = &ContainerCPUData{
						Container:              container,
						CPUCoresHours:          CPUCores * runHours,
						CPUCores:               CPUCores,
						CPUCoresRequestAverage: 0.0,
					}
				}

				// ----------------------------------------------
				// Gather CPURequestAverage (CPURequested) for every container in a Pod

				// Iterate over results and group by pod
				// ----------------------------------------------
				for _, CPURequestedItem := range requestedCPU.Data.Result {
					container := CPURequestedItem.Metric.Container
					pod := CPURequestedItem.Metric.Pod
					if container == "" {
						t.Logf("Skipping CPU allocation for empty container in pod %s in namespace: %s", pod, CPURequestedItem.Metric.Namespace)
						continue
					}
					podData, ok := podMap[pod]
					if !ok {
						t.Logf("[Skipping] Failed to find namespace: %s and pod: %s in CPU allocated results", CPURequestedItem.Metric.Namespace, pod)
						continue
					}

					CPUCoresRequestedAverage := CPURequestedItem.Value.Value

					runMinutes := podData.Minutes
					if runMinutes <= 0 {
						t.Logf("Namespace: %s, Pod %s has a run duration of 0 minutes, skipping CPU allocation calculation", podData.Namespace, podData.Pod)
						continue
					}

					runHours := utils.ConvertToHours(runMinutes)

					// if the container exists, you need to apply the opencost cost specification
					if containerData, ok := podData.Containers[container]; ok {
						if containerData.CPUCores < CPUCoresRequestedAverage {
							containerData.CPUCores = CPUCoresRequestedAverage
							containerData.CPUCoresHours = CPUCoresRequestedAverage * runHours
						}
					} else {
						podData.Containers[container] = &ContainerCPUData{
							Container:     container,
							CPUCoresHours: CPUCoresRequestedAverage * runHours,
						}
					}

					podData.Containers[container].CPUCoresRequestAverage = CPUCoresRequestedAverage
				}

				// ----------------------------------------------
				// Aggregate Container results to get Pod Output and Aggregate Pod Output to get Namespace results

				// Aggregating the AVG CPU values is not as simple as just summing them up because we have to consider that
				// each pod's average CPU data is relative to that same pod's lifetime. So, in order to aggregate the data
				// together, we have to expand the averages back into their pure Core values, merge the run times, sum the
				// raw values, and then REAPPLY the merged run time. See core/pkg/opencost/allocation.go "add()" line #1225
				// NOTE: This is only for the average CPU values, CPUCoreHours can be summed directly.
				// ----------------------------------------------
				nsCPUCoresRequest := 0.0
				nsCPUCoresHours := 0.0
				nsCPUCores := 0.0
				nsMinutes := 0.0
				var nsStart, nsEnd time.Time

				for _, podData := range podMap {

					start := podData.Start
					end := podData.End
					minutes := podData.Minutes

					CPUCoreRequest := 0.0
					CPUCoreHours := 0.0

					for _, containerData := range podData.Containers {
						CPUCoreHours += containerData.CPUCoresHours
						CPUCoreRequest += containerData.CPUCoresRequestAverage
					}
					// t.Logf("Pod %s, CPUCoreHours %v", podData.Pod, CPUCoreHours)
					// Sum up Pod Values
					nsCPUCoresRequest += (CPUCoreRequest*minutes + nsCPUCoresRequest*nsMinutes)
					nsCPUCoresHours += CPUCoreHours

					// only the first time
					if nsStart.IsZero() && nsEnd.IsZero() {
						nsStart = start
						nsEnd = end
						nsMinutes = nsEnd.Sub(nsStart).Minutes()
						nsHours := utils.ConvertToHours(nsMinutes)
						nsCPUCores = nsCPUCoresHours / nsHours
						nsCPUCoresRequest = nsCPUCoresRequest / nsMinutes
						continue
					} else {
						if start.Before(nsStart) {
							nsStart = start
						}
						if end.After(nsEnd) {
							nsEnd = end
						}
						nsMinutes = nsEnd.Sub(nsStart).Minutes()
						nsHours := utils.ConvertToHours(nsMinutes)
						nsCPUCores = nsCPUCoresHours / nsHours
						nsCPUCoresRequest = nsCPUCoresRequest / nsMinutes
					}
				}

				if nsMinutes < cpuCorevsCpuRequestShortLivedPodsRunTime {
					// Too short of a run time to assert results. ByteHours is very sensitive to run time.
					t.Logf("[Skipped] Namespace %v: RunTime %v less than %v ", namespace, nsMinutes, cpuCorevsCpuRequestShortLivedPodsRunTime)
					continue
				}

				// ----------------------------------------------
				// Compare Results with Allocation
				// ----------------------------------------------
				t.Logf("Namespace: %s", namespace)
				// 5 % Tolerance
				if allocationResponseItem.CPUCores > cpuCorevsCpuRequestnegligibleCores {
					withinRange, diff_percent := utils.AreWithinPercentage(nsCPUCores, allocationResponseItem.CPUCores, cpuCorevsCpuRequestTolerance)
					if withinRange {
						t.Logf("    - CPUCores[Pass]: ~%.2f", nsCPUCores)
					} else {
						t.Errorf("    - CPUCores[Fail]: DifferencePercent: %0.2f, Prom Results: %.2f, API Results: %.2f", diff_percent, nsCPUCores, allocationResponseItem.CPUCores)
					}
				}
				if allocationResponseItem.CPUCoreHours > cpuCorevsCpuRequestnegligibleCores {
					withinRange, diff_percent := utils.AreWithinPercentage(nsCPUCoresHours, allocationResponseItem.CPUCoreHours, cpuCorevsCpuRequestTolerance)
					if withinRange {
						t.Logf("    - CPUCoreHours[Pass]: ~%.2f", nsCPUCoresHours)
					} else {
						t.Errorf("    - CPUCoreHours[Fail]: DifferencePercent: %0.2f, Prom Results: %.2f, API Results: %.2f", diff_percent, nsCPUCoresHours, allocationResponseItem.CPUCoreHours)
					}
				}
				if allocationResponseItem.CPUCoreRequestAverage > cpuCorevsCpuRequestnegligibleCores {
					withinRange, diff_percent := utils.AreWithinPercentage(nsCPUCoresRequest, allocationResponseItem.CPUCoreRequestAverage, cpuCorevsCpuRequestTolerance)
					if withinRange {
						t.Logf("    - CPUCoreRequestAverage[Pass]: ~%.2f", nsCPUCoresRequest)
					} else {
						t.Errorf("    - CPUCoreRequestAverage[Fail]: DifferencePercent: %0.2f, Prom Results: %.2f, API Results: %.2f", diff_percent, nsCPUCoresRequest, allocationResponseItem.CPUCoreRequestAverage)
					}
				}
			}
		})
	}
}
