package telemetry

import("sync";"time")
type Snapshot struct{RunnerRequests uint64 `json:"runnerRequests"`;LifecycleSuccess uint64 `json:"lifecycleSuccess"`;LifecycleFailure uint64 `json:"lifecycleFailure"`;ProvisionTotal time.Duration `json:"provisionTotal"`;CleanupTotal time.Duration `json:"cleanupTotal"`;QueueSamples uint64 `json:"queueSamples"`;QueueTotal time.Duration `json:"queueTotal"`};type Metrics struct{mu sync.Mutex;s Snapshot}
func(m *Metrics)RunnerRequested(){m.mu.Lock();m.s.RunnerRequests++;m.mu.Unlock()}
func(m *Metrics)Lifecycle(ok bool,provision,cleanup time.Duration){m.mu.Lock();defer m.mu.Unlock();if ok{m.s.LifecycleSuccess++}else{m.s.LifecycleFailure++};m.s.ProvisionTotal+=provision;m.s.CleanupTotal+=cleanup}
func(m *Metrics)Queue(d time.Duration){m.mu.Lock();m.s.QueueSamples++;m.s.QueueTotal+=d;m.mu.Unlock()}
func(m *Metrics)Snapshot()Snapshot{m.mu.Lock();defer m.mu.Unlock();return m.s}
type CostInput struct{Watts float64;Hours float64;EnergyPricePerKWh float64;HardwareMonthly float64;Jobs int};type CostEstimate struct{EnergyCost float64 `json:"energyCost"`;TotalCost float64 `json:"totalCost"`;CostPerJob float64 `json:"costPerJob"`}
func EstimateCost(i CostInput)CostEstimate{energy:=i.Watts*i.Hours/1000*i.EnergyPricePerKWh;total:=energy+i.HardwareMonthly;per:=0.0;if i.Jobs>0{per=total/float64(i.Jobs)};return CostEstimate{EnergyCost:energy,TotalCost:total,CostPerJob:per}}
func RecommendConcurrency(avgQueue time.Duration,utilization float64,current int)int{if current<1{current=1};if avgQueue>30*time.Second&&utilization>0.75{return current+1};if avgQueue<5*time.Second&&utilization<0.25&&current>1{return current-1};return current}
