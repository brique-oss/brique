/*
 * Copyright 2026 Nicolas Cassan
 * Licensed under the Apache License, Version 2.0.
 */

package comm

import (
	"context"
	"errors"
	"time"

	"brique_engine/junction"
	"brique_engine/signedtransport"
)

const signedTransportDeliveryTimeout = 10 * time.Second

func (l *CommLoop) signedTransportHub() *signedtransport.Hub {
	if l == nil || l.frame == nil || l.frame.CtxCommReg == nil {
		return nil
	}
	provider, ok := any(l.frame.CtxCommReg).(junction.SignedTransportProvider)
	if !ok {
		return nil
	}
	return provider.SignedTransport()
}

func (l *CommLoop) receiveSignedTransport(ctx context.Context, value signedtransport.IngressEnvelope) error {
	if l == nil || l.signedExternalIngress == nil || ctx == nil {
		return signedtransport.ErrUnavailable
	}
	item := signedExternalIngressItem{Interface: "browser-wss", RouteID: value.RouteID,
		RemoteInstanceID: value.RemoteInstanceID, RemoteKeyID: value.RemoteKeyID,
		Envelope: append([]byte(nil), value.Envelope...)}
	select {
	case l.signedExternalIngress <- item:
		return nil
	case <-l.done:
		return signedtransport.ErrUnavailable
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *CommLoop) loopSignedTransportEgress() {
	for {
		select {
		case <-l.done:
			return
		case item, ok := <-l.signedTransportEgress:
			if !ok {
				return
			}
			hub := l.signedTransportHub()
			if hub == nil {
				l.enqueueSignedExternalEgressOutcome(item, InterfaceEgressOutcome{Definitive: false, Err: signedtransport.ErrUnavailable})
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), signedTransportDeliveryTimeout)
			var err error
			if item.RouteID != "" {
				err = hub.SendRoute(ctx, item.RouteID, item.Envelope)
			} else {
				err = hub.SendTo(ctx, item.RemoteInstanceID, item.RemoteKeyID, item.Envelope)
			}
			cancel()
			outcome := InterfaceEgressOutcome{Definitive: err == nil || errors.Is(err, signedtransport.ErrRejected), Err: err}
			l.enqueueSignedExternalEgressOutcome(item, outcome)
		}
	}
}

func (l *CommLoop) enqueueSignedTransport(item signedExternalEgressItem) bool {
	if l == nil || l.signedTransportEgress == nil {
		return false
	}
	// A burst of answers must not be lost because the writer is a few places behind: the routing
	// loop waits for a place, but for a bounded time (the same rule as every other egress queue).
	outcome, _ := offer(l.signedTransportEgress, item, defaultEgressWait, l.done)
	return outcome == offerAccepted
}
