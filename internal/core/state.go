package core

var hostTransitions = map[HostState]map[HostState]struct{}{
	HostEnrolling: {HostOnline: {}, HostOffline: {}, HostError: {}},
	HostOnline:    {HostDraining: {}, HostOffline: {}, HostError: {}},
	HostDraining:  {HostOnline: {}, HostOffline: {}, HostError: {}},
	HostOffline:   {HostEnrolling: {}, HostOnline: {}, HostError: {}},
	HostError:     {HostEnrolling: {}, HostOffline: {}},
}

var workerTransitions = map[WorkerState]map[WorkerState]struct{}{
	WorkerRequested:      {WorkerProvisioning: {}, WorkerDeleting: {}, WorkerFailed: {}},
	WorkerProvisioning:   {WorkerStarting: {}, WorkerFailed: {}, WorkerDeleting: {}},
	WorkerStarting:       {WorkerRegistering: {}, WorkerFailed: {}, WorkerDeleting: {}},
	WorkerRegistering:    {WorkerReady: {}, WorkerFailed: {}, WorkerDeleting: {}},
	WorkerReady:          {WorkerBusy: {}, WorkerCompleted: {}, WorkerDeleting: {}, WorkerFailed: {}},
	WorkerBusy:           {WorkerCompleted: {}, WorkerFailed: {}, WorkerDeleting: {}},
	WorkerCompleted:      {WorkerDeleting: {}, WorkerCleanupPending: {}},
	WorkerFailed:         {WorkerCleanupPending: {}, WorkerDeleting: {}},
	WorkerCleanupPending: {WorkerDeleting: {}, WorkerDeleted: {}},
	WorkerDeleting:       {WorkerDeleted: {}, WorkerCleanupPending: {}},
	WorkerDeleted:        {},
}

func CanTransitionHost(from, to HostState) bool {
	_, ok := hostTransitions[from][to]
	return ok
}

func CanTransitionWorker(from, to WorkerState) bool {
	_, ok := workerTransitions[from][to]
	return ok
}
