/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package launcher

import (
	"context"
	"testing"

	"brique_engine/contracts/connectiondiagnosis"
	"brique_engine/engine/localruntime"
)

func TestLauncherForwardsAConnectionDiagnosisToTheRunningEngine(t *testing.T) {
	store, root, engine := testStore(t)
	instance, err := store.Add(context.Background(), AddOptions{Alias: "alpha", RootPath: root, EnginePath: engine, Supervision: SupervisionExternal})
	if err != nil {
		t.Fatal(err)
	}
	engineRuntime := shortControlDirectory(t)
	supervisor, err := NewSupervisor(SupervisorOptions{Store: store, RuntimeDirectory: engineRuntime})
	if err != nil {
		t.Fatal(err)
	}
	controlRuntime := shortControlDirectory(t)
	server, err := StartLauncherControl(supervisor, controlRuntime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	diagnose := func(peer, endpoint string) (LauncherControlResponse, error) {
		return CallLauncherControl(store.Directory, controlRuntime, LauncherControlRequest{
			Operation: "diagnose_connection", Alias: "alpha", Peer: peer, Endpoint: endpoint})
	}

	if _, err := diagnose("peer-a", ""); err == nil {
		t.Fatal("a diagnosis was answered although the engine is not running")
	}

	generation, err := localruntime.NewGeneration()
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	control, err := localruntime.StartControl(localruntime.ControlOptions{DiscoveryDirectory: engineRuntime, RootPath: instance.RootPath,
		RootObject: instance.RootObject, Generation: generation,
		Diagnose: func(_ context.Context, peer, endpoint string) connectiondiagnosis.Result {
			asked = append(asked, peer+"|"+endpoint)
			return connectiondiagnosis.Refused(peer, endpoint, connectiondiagnosis.StepTCP, connectiondiagnosis.CodeConnectionRefused, "closed")
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	if err := control.SetState(localruntime.StateReady); err != nil {
		t.Fatal(err)
	}

	response, err := diagnose("peer-a", "paris")
	if err != nil || response.Diagnosis == nil || response.Diagnosis.PeerID != "peer-a" || response.Diagnosis.OK ||
		len(asked) != 1 || asked[0] != "peer-a|paris" {
		t.Fatalf("diagnosis = %#v, %v, asked %v", response, err, asked)
	}
	for _, request := range []LauncherControlRequest{
		{Operation: "diagnose_connection", Alias: "alpha"},
		{Operation: "diagnose_connection", Alias: "alpha", Peer: "https://10.0.0.1/"},
		{Operation: "diagnose_connection", Alias: "alpha", Peer: "peer-a", Endpoint: "a/b"},
		{Operation: "diagnose_connection", Peer: "peer-a"},
		{Operation: "status", Alias: "alpha", Peer: "peer-a"},
	} {
		if _, err := CallLauncherControl(store.Directory, controlRuntime, request); err == nil {
			t.Fatalf("request accepted: %+v", request)
		}
	}
	if len(asked) != 1 {
		t.Fatalf("a refused request reached the engine: %v", asked)
	}
}
