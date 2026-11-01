/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package comm

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"brique_engine/circulation"
	"brique_engine/junction"
	"brique_engine/shared"
)

type trackingCommReg struct {
	mu sync.Mutex

	chByID    map[shared.ContextAddr]chan junction.Delivery
	extByID   map[shared.ContextAddr]string
	uiByName  map[string]shared.ContextAddr
	wrpByName map[string]shared.ContextAddr

	registerCalls   int
	unregisterCalls int
}

func newTrackingCommReg() *trackingCommReg {
	return &trackingCommReg{
		chByID:    map[shared.ContextAddr]chan junction.Delivery{},
		extByID:   map[shared.ContextAddr]string{},
		uiByName:  map[string]shared.ContextAddr{},
		wrpByName: map[string]shared.ContextAddr{},
	}
}

func (m *trackingCommReg) Register(addr shared.ContextAddr, _ chan<- junction.Delivery, extName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registerCalls++
	if extName != "" {
		m.extByID[addr] = extName
	}
}
func (m *trackingCommReg) Unregister(addr shared.ContextAddr, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unregisterCalls++
	delete(m.extByID, addr)
}
func (m *trackingCommReg) ResolveCh(addr shared.ContextAddr) (chan<- junction.Delivery, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.chByID[addr]
	if !ok {
		return nil, false
	}
	return ch, true
}
func (m *trackingCommReg) ResolveExtName(extName string) (shared.ContextAddr, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, ext := range m.extByID {
		if ext == extName {
			return id, true
		}
	}
	return "", false
}
func (m *trackingCommReg) ResolveIDToExtName(id shared.ContextAddr) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ext, ok := m.extByID[id]
	return ext, ok
}
func (m *trackingCommReg) RegisterUI(uiName string, ctxID shared.ContextAddr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.uiByName[uiName] = ctxID
}
func (m *trackingCommReg) UnregisterUI(uiName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uiByName, uiName)
}
func (m *trackingCommReg) UnregisterUIOwner(uiName string, ctxID shared.ContextAddr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uiByName[uiName] == ctxID {
		delete(m.uiByName, uiName)
	}
}
func (m *trackingCommReg) ResolveUI(uiName string) (shared.ContextAddr, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.uiByName[uiName]
	return v, ok
}
func (m *trackingCommReg) ResolveUIInScope(uiName string, scopeCtxID shared.ContextAddr) (shared.ContextAddr, bool) {
	_ = scopeCtxID
	return m.ResolveUI(uiName)
}
func (m *trackingCommReg) RegisterWrapperBoundary(wrapperName string, boundaryCtxID shared.ContextAddr) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wrpByName[wrapperName] = boundaryCtxID
	return nil
}
func (m *trackingCommReg) UnregisterWrapperBoundary(wrapperName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.wrpByName, wrapperName)
}
func (m *trackingCommReg) ResolveWrapperBoundary(wrapperName string) (shared.ContextAddr, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.wrpByName[wrapperName]
	return v, ok
}

type fakeInterfaceImpl struct {
	startErr   error
	readErr    error
	writeErr   error
	readExit   bool
	writeExit  bool
	readReturn chan struct{}
	dirs       InterfaceDirections

	closeCh chan struct{}

	mu         sync.Mutex
	startCalls int
	readCalls  int
	writeCalls int
	closeCalls int
}

func newFakeInterfaceImpl() *fakeInterfaceImpl {
	return &fakeInterfaceImpl{
		closeCh: make(chan struct{}),
		dirs:    InterfaceDirections{ReadLoop: true, WriteLoop: true},
	}
}

func (f *fakeInterfaceImpl) Directions() InterfaceDirections { return f.dirs }

func (f *fakeInterfaceImpl) Start() error {
	f.mu.Lock()
	f.startCalls++
	f.mu.Unlock()
	return f.startErr
}

func (f *fakeInterfaceImpl) ReadLoop() error {
	f.mu.Lock()
	f.readCalls++
	exit := f.readExit
	err := f.readErr
	f.mu.Unlock()
	if exit {
		return err
	}
	if f.readReturn != nil {
		<-f.readReturn
		return err
	}
	<-f.closeCh
	return err
}

func (f *fakeInterfaceImpl) WriteLoop() error {
	f.mu.Lock()
	f.writeCalls++
	exit := f.writeExit
	err := f.writeErr
	f.mu.Unlock()
	if exit {
		return err
	}
	<-f.closeCh
	return err
}

func (f *fakeInterfaceImpl) Close() error {
	f.mu.Lock()
	f.closeCalls++
	f.mu.Unlock()
	select {
	case <-f.closeCh:
	default:
		close(f.closeCh)
	}
	return nil
}

func (f *fakeInterfaceImpl) stats() (start, read, write, close int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCalls, f.readCalls, f.writeCalls, f.closeCalls
}

func waitPred(t *testing.T, timeout time.Duration, pred func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", label)
}

func newCommLoopHarness(ctxID string, reg junction.ContextCommInRegistry) *CommLoop {
	return &CommLoop{
		frame: &junction.ContextRegistry{
			CtxId:      ctxID,
			CtxExtName: "ctx-ext",
			CtxCommReg: reg,
			FamIn:      junction.FamiliesInChanRegistry{},
		},
		in:      make(chan junction.Delivery, 8),
		ctxIn:   make(chan junction.Delivery, 8),
		ingress: make(chan IngressItem, 8),
		ifaces:  map[string]*InterfaceRuntime{},
		done:    make(chan struct{}),
		state:   shared.FamilyInitializing,
		cfg: CommCfg{
			WrapperBoundary: []string{"w1"},
		},
	}
}

func TestCommLoop_N1_CML_01_NewCommLoopInitializes(t *testing.T) {
	frame := &junction.ContextRegistry{}
	l := NewCommLoop(frame, nil)
	if l == nil || l.frame != frame {
		t.Fatalf("loop/frame init failed")
	}
	if l.in == nil || l.ctxIn == nil || l.ingress == nil || l.interfaceOutcomes == nil || l.done == nil {
		t.Fatalf("expected initialized runtime channels")
	}
	if l.State() != shared.FamilyInitializing {
		t.Fatalf("state = %v, want initializing", l.State())
	}
	if l.ifaces == nil {
		t.Fatalf("ifaces map should be initialized")
	}
}

func TestCommLoop_N1_CML_02_StartGuards(t *testing.T) {
	reg := newTrackingCommReg()
	l := newCommLoopHarness("/ctx/a", reg)

	l.state = shared.FamilyRunning
	l.Start()
	if l.State() != shared.FamilyRunning {
		t.Fatalf("running guard should keep running")
	}

	l.state = shared.FamilyStopped
	l.Start()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("stopped guard should keep stopped")
	}
}

func TestCommLoop_N1_CML_03_StartRegistersAndRuns(t *testing.T) {
	reg := newTrackingCommReg()
	l := newCommLoopHarness("/ctx/a", reg)
	impl := newFakeInterfaceImpl()
	l.ifaces["ui-main"] = &InterfaceRuntime{Name: "ui-main", Impl: impl}
	l.cfg.Ifaces = append(l.cfg.Ifaces, InterfaceCfg{Name: "ui-main", Type: EndpointUI})

	l.Start()
	waitPred(t, time.Second, func() bool { return l.State() == shared.FamilyRunning }, "comm loop running")

	start, _, _, _ := impl.stats()
	if start != 1 {
		t.Fatalf("interface Start calls=%d want 1", start)
	}
	waitPred(t, time.Second, func() bool {
		_, r, w, _ := impl.stats()
		return r >= 1 && w >= 1
	}, "interface read/write loops started")
	_, read, write, _ := impl.stats()
	if read < 1 || write < 1 {
		t.Fatalf("expected read/write loops started, got read=%d write=%d", read, write)
	}

	reg.mu.Lock()
	_, okExt := reg.extByID["/ctx/a"]
	uiOwner, okUI := reg.uiByName["ui-main"]
	wrpOwner, okW := reg.wrpByName["w1"]
	regCalls := reg.registerCalls
	reg.mu.Unlock()
	if !okExt || regCalls == 0 {
		t.Fatalf("expected context registration in registry")
	}
	if !okUI || uiOwner != "/ctx/a" {
		t.Fatalf("expected ui registration, got (%v,%q)", okUI, uiOwner)
	}
	if !okW || wrpOwner != "/ctx/a" {
		t.Fatalf("expected wrapper boundary registration, got (%v,%q)", okW, wrpOwner)
	}

	l.Stop()
}

func TestCommLoop_N1_CML_04_05_StopUnregistersAndIdempotent(t *testing.T) {
	reg := newTrackingCommReg()
	l := newCommLoopHarness("/ctx/a", reg)
	impl := newFakeInterfaceImpl()
	l.ifaces["i1"] = &InterfaceRuntime{Name: "i1", Impl: impl}
	l.Start()
	waitPred(t, time.Second, func() bool { return l.State() == shared.FamilyRunning }, "running before stop")

	l.Stop()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("state after stop = %v want stopped", l.State())
	}
	_, _, _, closeCalls := impl.stats()
	if closeCalls < 1 {
		t.Fatalf("expected interface close called")
	}

	reg.mu.Lock()
	_, okExt := reg.extByID["/ctx/a"]
	_, okUI := reg.uiByName["ui-main"]
	_, okW := reg.wrpByName["w1"]
	unregCalls := reg.unregisterCalls
	reg.mu.Unlock()
	if okExt || okUI || okW || unregCalls == 0 {
		t.Fatalf("expected unregister and wrapper unbind on stop")
	}

	// idempotent second stop
	l.Stop()
	if l.State() != shared.FamilyStopped {
		t.Fatalf("second stop should keep stopped")
	}
}

func TestCommLoop_N1_CML_06_StartInterfaceFailureMarksFailed(t *testing.T) {
	reg := newTrackingCommReg()
	l := newCommLoopHarness("/ctx/a", reg)
	impl := newFakeInterfaceImpl()
	impl.startErr = errors.New("start fail")
	l.ifaces["i1"] = &InterfaceRuntime{Name: "i1", Impl: impl}

	l.Start()
	waitPred(t, time.Second, func() bool { return l.State() == shared.FamilyRunning }, "running with failed iface")

	rt := l.ifaces["i1"]
	state := rt.State()
	if state.Status != InterfaceFailed || state.LastErr == nil {
		t.Fatalf("failed interface should be marked failed with error, got status=%v err=%v", state.Status, state.LastErr)
	}
	_, read, write, _ := impl.stats()
	if read != 0 || write != 0 {
		t.Fatalf("failed interface must not start read/write loops")
	}

	l.Stop()
}

func TestCommLoop_InterfaceDirectionsStartOnlyRequiredLoops(t *testing.T) {
	l := newCommLoopHarness("/ctx/a", newTrackingCommReg())
	impl := newFakeInterfaceImpl()
	impl.dirs = InterfaceDirections{WriteLoop: true}
	l.ifaces["egress"] = &InterfaceRuntime{Name: "egress", Impl: impl}

	l.Start()
	waitPred(t, time.Second, func() bool {
		_, read, write, _ := impl.stats()
		return read == 0 && write == 1
	}, "only required interface loop started")
	if state := l.ifaces["egress"].State(); state.Status != InterfaceRunning {
		t.Fatalf("interface state=%v want running", state.Status)
	}
	l.Stop()
}

func TestCommLoop_UnexpectedInterfaceLoopExitMarksOnlyInterfaceFailed(t *testing.T) {
	l := newCommLoopHarness("/ctx/a", newTrackingCommReg())
	impl := newFakeInterfaceImpl()
	impl.dirs = InterfaceDirections{ReadLoop: true}
	impl.readExit = true
	impl.readErr = errors.New("read transport failed")
	l.ifaces["broken"] = &InterfaceRuntime{Name: "broken", Impl: impl}

	l.Start()
	waitPred(t, time.Second, func() bool {
		return l.ifaces["broken"].State().Status == InterfaceFailed
	}, "interface failure after loop exit")
	state := l.ifaces["broken"].State()
	if state.LastErr == nil || !strings.Contains(state.LastErr.Error(), "read transport failed") {
		t.Fatalf("missing preserved loop error: %v", state.LastErr)
	}
	if l.State() != shared.FamilyRunning {
		t.Fatalf("interface failure must not stop communication family: %v", l.State())
	}
	_, _, _, closeCalls := impl.stats()
	if closeCalls != 1 {
		t.Fatalf("failed interface close calls=%d want 1", closeCalls)
	}
	l.Stop()
	if state := l.ifaces["broken"].State(); state.Status != InterfaceClosed || state.LastErr == nil {
		t.Fatalf("stop must close while preserving failure, got %#v", state)
	}
}

func TestCommLoop_FailedUIIsNotPublishedButContextAndBoundaryRemain(t *testing.T) {
	reg := newTrackingCommReg()
	l := newCommLoopHarness("/ctx/a", reg)
	impl := newFakeInterfaceImpl()
	impl.startErr = errors.New("ui bind failed")
	l.ifaces["ui-main"] = &InterfaceRuntime{Name: "ui-main", Impl: impl}
	l.cfg.Ifaces = []InterfaceCfg{{Name: "ui-main", Type: EndpointUI}}

	l.Start()
	reg.mu.Lock()
	_, contextRegistered := reg.extByID["/ctx/a"]
	_, uiRegistered := reg.uiByName["ui-main"]
	boundary, boundaryRegistered := reg.wrpByName["w1"]
	reg.mu.Unlock()
	if !contextRegistered {
		t.Fatal("failed external interface must not hide the context")
	}
	if uiRegistered {
		t.Fatal("failed UI interface must not be published")
	}
	if !boundaryRegistered || boundary != "/ctx/a" {
		t.Fatalf("wrapper ownership must remain published, got (%v,%q)", boundaryRegistered, boundary)
	}
	l.Stop()
}

func TestCommLoop_RunningUIIsUnpublishedWhenItsLoopFails(t *testing.T) {
	reg := newTrackingCommReg()
	l := newCommLoopHarness("/ctx/a", reg)
	impl := newFakeInterfaceImpl()
	impl.dirs = InterfaceDirections{ReadLoop: true}
	impl.readReturn = make(chan struct{})
	impl.readErr = errors.New("ui transport failed")
	l.ifaces["ui-main"] = &InterfaceRuntime{Name: "ui-main", Type: EndpointUI, Impl: impl}
	l.cfg.Ifaces = []InterfaceCfg{{Name: "ui-main", Type: EndpointUI}}

	l.Start()
	if owner, ok := reg.ResolveUI("ui-main"); !ok || owner != "/ctx/a" {
		t.Fatalf("running UI should be published, got (%v,%q)", ok, owner)
	}
	close(impl.readReturn)
	waitPred(t, time.Second, func() bool {
		_, ok := reg.ResolveUI("ui-main")
		return !ok
	}, "failed UI removal")
	if _, ok := reg.ResolveExtName("ctx-ext"); !ok {
		t.Fatal("UI failure must not unregister its context")
	}
	l.Stop()
}

func TestCommLoop_N1_CML_07_LoopContextIngressRoutesEgress(t *testing.T) {
	reg := newTrackingCommReg()
	dstCh := make(chan junction.Delivery, 1)
	reg.chByID["/ctx/dst"] = dstCh
	l := newCommLoopHarness("/ctx/src", reg)
	traceCh := make(chan junction.Delivery, 8)
	l.frame.FamIn[shared.FamilyTrace] = traceCh

	exit := make(chan struct{})
	go func() { defer close(exit); l.loopContextIngress() }()

	l.in <- l.frame.MustDelivery(mkIntentionMsg("/ctx/dst", circulation.ValueTypeMatter, ""))
	_ = recvMsg(t, dstCh, "context ingress routed")
	close(l.done)
	waitPred(t, time.Second, func() bool {
		select {
		case <-exit:
			return true
		default:
			return false
		}
	}, "loopContextIngress exit")
}

func TestCommLoop_N1_CML_08_LoopInterContextIngressRoutesIngress(t *testing.T) {
	reg := newTrackingCommReg()
	l := newCommLoopHarness("/ctx/local", reg)
	matterCh := make(chan junction.Delivery, 1)
	traceCh := make(chan junction.Delivery, 8)
	l.frame.FamIn[shared.FamilyMatter] = matterCh
	l.frame.FamIn[shared.FamilyTrace] = traceCh

	exit := make(chan struct{})
	go func() { defer close(exit); l.loopInterContextIngress() }()

	l.ctxIn <- l.frame.MustDelivery(mkIntentionMsg("/ctx/local", circulation.ValueTypeMatter, "/ctx/src"))
	_ = recvMsg(t, matterCh, "inter-context ingress routed")
	close(l.done)
	waitPred(t, time.Second, func() bool {
		select {
		case <-exit:
			return true
		default:
			return false
		}
	}, "loopInterContextIngress exit")
}

func TestCommLoop_N1_CML_09_LoopInterfaceIngressRoutesIngress(t *testing.T) {
	reg := newTrackingCommReg()
	l := newCommLoopHarness("/ctx/local", reg)
	ctrlCh := make(chan junction.Delivery, 1)
	traceCh := make(chan junction.Delivery, 8)
	l.frame.CtrlIn = ctrlCh
	l.frame.FamIn[shared.FamilyTrace] = traceCh

	exit := make(chan struct{})
	go func() { defer close(exit); l.loopInterfaceIngress() }()

	l.ingress <- IngressItem{
		Endpoint:  EndpointUI,
		IfaceName: "ui1",
		Msg:       mkIntentionMsg("/ctx/local", circulation.ValueTypeControl, ""),
	}
	_ = recvMsg(t, ctrlCh, "interface ingress control routed")
	close(l.done)
	waitPred(t, time.Second, func() bool {
		select {
		case <-exit:
			return true
		default:
			return false
		}
	}, "loopInterfaceIngress exit")
}
