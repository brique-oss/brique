/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package admission_test

import (
	"errors"
	"testing"
	"time"

	"brique_engine/engine/circulation"
	"brique_engine/engine/junction"
	"brique_engine/engine/security/trust"
)

func TestPublicWebAdmissionManufacturesSourceAndSurvivesDerivation(t *testing.T) {
	fixture := newVerbFixtureWith(t, func(policy *trust.Policy) {
		policy.FormatVersion = trust.PolicyFormatVersionV4
		policy.Principals = append(policy.Principals, trust.Principal{
			PrincipalID: trust.PublicWebPrincipalID, Kind: trust.PrincipalKindPublic, Status: "active",
			ScopeIDs: []string{"verbs"}, SigningKeys: []trust.SigningKey{},
		})
	})
	public, err := fixture.runtime.AdmitPublicWeb()
	if err != nil {
		t.Fatal(err)
	}
	request := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "public-root", AwaitResponse: true,
		To:       circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
		From:     circulation.Address{Context: "/forged", Type: "admin", Cap: "control"},
		Identity: circulation.Identity{ID: "forged", Kind: "instance"},
	}}
	delivery, err := junction.AdmitPublicWeb(public, "channel_1", "root", request, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	message, ok := delivery.Message()
	context, isPublic := delivery.PublicWeb()
	if !ok || !isPublic || !context.IsPublicWeb() || context.ChannelID() != "channel_1" {
		t.Fatal("public delivery lost its protected admission")
	}
	if message.Intention.From.Context != "@public_web:/channel_1/root" || message.Intention.From.Type != "public" ||
		message.Intention.From.Cap != "web" || message.Intention.Identity != (circulation.Identity{}) {
		t.Fatalf("manufactured source = %#v, identity = %#v", message.Intention.From, message.Intention.Identity)
	}
	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "public-child", To: circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
		Correlation: &circulation.Correlation{RootIntentionID: "public-root", ParentIntentionID: "public-root"},
	}}
	derived, err := junction.DeriveExternal(delivery, child, fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	derivedContext, isPublic := derived.PublicWeb()
	if !isPublic || derivedContext != context {
		t.Fatal("derived intention lost public authority or channel ownership")
	}
}

func TestPublicWebResponseReturnsToTheManufacturedChannel(t *testing.T) {
	fixture := newVerbFixtureWith(t, func(policy *trust.Policy) {
		policy.FormatVersion = trust.PolicyFormatVersionV4
		policy.Principals = append(policy.Principals, trust.Principal{
			PrincipalID: trust.PublicWebPrincipalID, Kind: trust.PrincipalKindPublic, Status: "active",
			ScopeIDs: []string{"verbs"}, SigningKeys: []trust.SigningKey{},
		})
	})
	public, _ := fixture.runtime.AdmitPublicWeb()
	request, err := junction.AdmitPublicWeb(public, "channel_2", "root", circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "public-response", AwaitResponse: true,
		To: circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
	}}, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: "public-response", Status: circulation.ValueStatusOK,
	}}
	bound, reservation, err := junction.BindExternalResponse(request, response)
	if err != nil || reservation.Valid() {
		t.Fatalf("BindExternalResponse = reservation %v, err %v", reservation.Valid(), err)
	}
	boundMessage, ok := bound.Message()
	source, local := bound.LocalAuthoritySource()
	if !ok || !local || source != "public:"+trust.PublicWebPrincipalID || boundMessage.Response.To.Context != "@public_web:/channel_2/root" {
		t.Fatalf("bound public response = %#v, source %q", boundMessage, source)
	}
}

func TestPublicWebAdmissionFailsClosedWithoutConfiguration(t *testing.T) {
	fixture := newVerbFixture(t)
	if _, err := fixture.runtime.AdmitPublicWeb(); !errors.Is(err, trust.ErrPublicWebUnavailable) {
		t.Fatalf("policy without public.web error = %v", err)
	}
}

func TestLocalUIAdmissionUsesExplicitPolicyAndProtectedChannel(t *testing.T) {
	fixture := newVerbFixtureWith(t, func(policy *trust.Policy) {
		policy.Principals = append(policy.Principals, trust.Principal{
			PrincipalID: trust.LocalUIPrincipalID, Kind: trust.PrincipalKindLocal, Status: "active",
			ScopeIDs: []string{"verbs"}, SigningKeys: []trust.SigningKey{},
		})
	})
	localUI, err := fixture.runtime.AdmitLocalUI()
	if err != nil {
		t.Fatal(err)
	}
	request := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "local-ui-root", AwaitResponse: true,
		To:       circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
		From:     circulation.Address{Context: "/forged", Type: "admin", Cap: "control"},
		Identity: circulation.Identity{ID: "forged", Kind: "instance"},
	}}
	delivery, err := junction.AdmitLocalUI(localUI, "local_channel", "root", request, fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := delivery.Message()
	context, admitted := delivery.LocalUI()
	if !ok || !admitted || !context.IsLocalUI() || context.IsPublicWeb() || context.IsRestrictedLocal() || context.ChannelID() != "local_channel" {
		t.Fatal("local UI delivery lost its distinct admission or channel")
	}
	if message.Intention.From.Context != "@local_ui:/local_channel/root" || message.Intention.From.Type != "local_ui" ||
		message.Intention.Identity != (circulation.Identity{}) {
		t.Fatalf("local UI source = %#v, identity = %#v", message.Intention.From, message.Intention.Identity)
	}
	if !context.AuthorizeCapability(fixture.runtime, "/root/catalog", "matter", "matter.read") ||
		context.AuthorizeCapability(fixture.runtime, "/root/catalog", "matter", "matter.write") {
		t.Fatal("local UI did not use its policy authority")
	}
	child := circulation.Message{Kind: circulation.ValueKindIntention, Intention: circulation.Intention{
		IntentionID: "local-ui-child", To: circulation.Address{Context: "/root/catalog", Type: "matter", Cap: "matter.read"},
		Correlation: &circulation.Correlation{RootIntentionID: "local-ui-root", ParentIntentionID: "local-ui-root"},
	}}
	derived, err := junction.DeriveExternal(delivery, child, fixture.runtime)
	if err != nil {
		t.Fatal(err)
	}
	if derivedContext, local := derived.LocalUI(); !local || derivedContext != context {
		t.Fatal("derived local UI intention lost authority or channel")
	}
	response := circulation.Message{Kind: circulation.ValueKindResponse, Response: circulation.Response{
		IntentionID: "local-ui-root", Status: circulation.ValueStatusOK,
	}}
	bound, reservation, err := junction.BindExternalResponse(delivery, response)
	if err != nil || reservation.Valid() {
		t.Fatalf("BindExternalResponse = reservation %v, err %v", reservation.Valid(), err)
	}
	boundMessage, valid := bound.Message()
	source, local := bound.LocalAuthoritySource()
	if !valid || !local || source != "local-ui:"+trust.LocalUIPrincipalID || boundMessage.Response.To.Context != "@local_ui:/local_channel/root" {
		t.Fatalf("bound local UI response = %#v, source %q", boundMessage, source)
	}
}

func TestLocalUIAdmissionFailsClosedWithoutConfiguration(t *testing.T) {
	fixture := newVerbFixture(t)
	if _, err := fixture.runtime.AdmitLocalUI(); !errors.Is(err, trust.ErrLocalUIUnavailable) {
		t.Fatalf("policy without local.ui error = %v", err)
	}
}
