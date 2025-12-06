
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/gosnmp/gosnmp"
	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type subscription struct {
	id   string
	ch   chan<- *gosnmp.SnmpPacket
	path *gnmi.Path
}
type subscriptionManager struct {
	sync.RWMutex
	subs map[string]map[string]*subscription
}
func newSubscriptionManager() *subscriptionManager {
	return &subscriptionManager{subs: make(map[string]map[string]*subscription)}
}
var subManager = newSubscriptionManager()
func (sm *subscriptionManager) Subscribe(targetIP string, path *gnmi.Path) (string, <-chan *gosnmp.SnmpPacket) {
	sm.Lock()
	defer sm.Unlock()
	subID := uuid.New().String()
	ch := make(chan *gosnmp.SnmpPacket, 10)
	if _, ok := sm.subs[targetIP]; !ok {
		sm.subs[targetIP] = make(map[string]*subscription)
	}
	sm.subs[targetIP][subID] = &subscription{id: subID, ch: ch, path: path}
	log.Printf("New subscription added: target=%s, subID=%s", targetIP, subID)
	return subID, ch
}
func (sm *subscriptionManager) Unsubscribe(targetIP string, subID string) {
	sm.Lock()
	defer sm.Unlock()
	if subs, ok := sm.subs[targetIP]; ok {
		if sub, subOK := subs[subID]; subOK {
			close(sub.ch)
			delete(subs, subID)
			log.Printf("Subscription removed: target=%s, subID=%s", targetIP, subID)
		}
		if len(subs) == 0 {
			delete(sm.subs, targetIP)
		}
	}
}
func (sm *subscriptionManager) Publish(sourceP string, packet *gosnmp.SnmpPacket) {
	sm.RLock()
	defer sm.RUnlock()
	if subs, ok := sm.subs[sourceP]; ok {
		log.Printf("Publishing trap from %s to %d subscriber(s)", sourceP, len(subs))
		for _, sub := range subs {
			select {
			case sub.ch <- packet:
			default:
				log.Printf("Warning: subscriber channel full, dropping trap. subID=%s", sub.id)
			}
		}
	}
}

type server struct {
	gnmi.UnimplementedGNMIServer
}

func (s *server) Get(ctx context.Context, req *gnmi.GetRequest) (*gnmi.GetResponse, error) {

	log.Printf("Received gNMI GetRequest: %v", req)
	if len(req.GetPath()) == 0 {
		return nil, fmt.Errorf("request does not contain any paths")
	}
	path := req.GetPath()[0]
	targetStr := path.GetTarget()
	if targetStr == "" {
		originParts := strings.SplitN(path.GetOrigin(), "=", 2)
		if len(originParts) == 2 {
			targetStr = originParts[1]
		}
	}
	if targetStr == "" {
		return nil, fmt.Errorf("target not specified in request path")
	}
	var targetIP string
	community := "public"
	version := gosnmp.Version2c
	params := strings.Split(targetStr, ",")
	if len(params) > 0 {
		targetIP = params[0]
	}
	for i := 1; i < len(params); i++ {
		kv := strings.SplitN(params[i], "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		value := strings.TrimSpace(kv[1])
		switch key {
		case "community":
			community = value
		case "version":
			switch strings.ToLower(value) {
			case "v1", "1":
				version = gosnmp.Version1
			case "v2c", "2", "2c":
				version = gosnmp.Version2c
			case "v3", "3":
				version = gosnmp.Version3
			default:
				log.Printf("Warning: unsupported SNMP version '%s'. Using default v2c.", value)
			}
		}
	}
	if targetIP == "" {
		return nil, fmt.Errorf("could not parse target IP address from '%s'", targetStr)
	}
	log.Printf("Target IP: '%s', Community: '%s', Version: %s", targetIP, community, version.String())
	var oids []string
	pathMap := make(map[string]*gnmi.Path)
	for _, p := range req.GetPath() {
		oid := gnmiPathToOidString(p)
		if oid != "" {
			oids = append(oids, oid)
			pathMap[oid] = p
		}
	}
	if len(oids) == 0 {
		return nil, fmt.Errorf("could not extract any valid OIDs from request")
	}
	log.Printf("Sending SNMP GetRequest (Target: %s, OIDs: %v)", targetIP, oids)
	result, err := sendSnmpGet(targetIP, community, version, oids)
	if err != nil {
		return nil, fmt.Errorf("SNMP Get failed: %w", err)
	}
	notifications := []*gnmi.Notification{}
	timestamp := time.Now().UnixNano()
	for _, variable := range result.Variables {
		originalPath, ok := pathMap[variable.Name[1:]]
		if !ok {
			log.Printf("Warning: could not find corresponding gNMI Path for OID: %s", variable.Name)
			continue
		}
		update := &gnmi.Update{
			Path: originalPath,
			Val:  snmpVarToGnmiTypedValue(variable),
		}
		notification := &gnmi.Notification{
			Timestamp: timestamp,
			Update:    []*gnmi.Update{update},
		}
		notifications = append(notifications, notification)
	}
	return &gnmi.GetResponse{Notification: notifications}, nil
}

func (s *server) Subscribe(stream gnmi.GNMI_SubscribeServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	log.Printf("Received initial gNMI SubscribeRequest: %v", req)
	subList := req.GetSubscribe()
	if subList == nil || len(subList.GetSubscription()) == 0 {
		return fmt.Errorf("invalid initial SubscribeRequest")
	}

	sub := subList.GetSubscription()[0]
	originalPath := sub.GetPath()


	targetIP := originalPath.GetTarget()

	if targetIP == "" {
		originParts := strings.SplitN(originalPath.GetOrigin(), "=", 2)
		if len(originParts) == 2 {
			targetIP = originParts[1]
		}
	}

	if targetIP == "" {
		if len(originalPath.GetElem()) > 0 {
			elemName := originalPath.GetElem()[0].GetName()
			if strings.HasPrefix(elemName, "target=") {
				targetIP = strings.TrimPrefix(elemName, "target=")
			}
		}
	}

	if targetIP == "" {
		return fmt.Errorf("could not extract target IP from subscribe path")
	}


	subID, ch := subManager.Subscribe(targetIP, originalPath)
	defer subManager.Unsubscribe(targetIP, subID)

	log.Printf("Client subscribed to traps from %s", targetIP)

	for {
		select {
		case <-stream.Context().Done():
			log.Printf("Client disconnected. subID=%s", subID)
			return nil
		case packet, ok := <-ch:
			if !ok {
				return nil
			}
			notification := snmpPacketToGnmiNotification(packet, originalPath)
			if err := stream.Send(&gnmi.SubscribeResponse{
				Response: &gnmi.SubscribeResponse_Update{Update: notification},
			}); err != nil {
				log.Printf("Failed to send trap notification: %v", err)
				return err
			}
		}
	}
}

func (s *server) Set(ctx context.Context, req *gnmi.SetRequest) (*gnmi.SetResponse, error) {
	log.Printf("Received gNMI SetRequest: %v", req)


	pdusByTarget := make(map[string][]gosnmp.SnmpPDU)
	pathToUpdate := make(map[string]*gnmi.Update)


	for _, update := range req.GetUpdate() {

		path := update.GetPath()
		targetStr := path.GetTarget()
		if targetStr == "" {
			originParts := strings.SplitN(path.GetOrigin(), "=", 2)
			if len(originParts) == 2 {
				targetStr = originParts[1]
			}
		}

		oid := gnmiPathToOidString(path)
		if oid == "" {
			return nil, status.Errorf(codes.InvalidArgument, "path does not contain a valid OID: %v", path)
		}


		pdu, err := gnmiTypedValueToSnmpPDU(oid, update.GetVal())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "failed to convert gNMI value to SNMP PDU: %v", err)
		}


		pdusByTarget[targetStr] = append(pdusByTarget[targetStr], *pdu)
		pathToUpdate[targetStr] = update
	}


	results := []*gnmi.UpdateResult{}
	timestamp := time.Now().UnixNano()


	for targetStr, pdus := range pdusByTarget {


		var targetIP string
		community := "public"
		version := gosnmp.Version2c
		params := strings.Split(targetStr, ",")
		if len(params) > 0 {
			targetIP = params[0]
		}
		for i := 1; i < len(params); i++ {
			kv := strings.SplitN(params[i], "=", 2)
			if len(kv) != 2 { continue }
			key := strings.ToLower(strings.TrimSpace(kv[0]))
			value := strings.TrimSpace(kv[1])
			if key == "community" { community = value }
			if key == "version" { version = parseSnmpVersion(value) }
		}

		log.Printf("Sending SNMP SetRequest (Target: %s, PDUs: %d)", targetIP, len(pdus))
		_, err := sendSnmpSet(targetIP, community, version, pdus)
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "SNMP Set failed for target %s: %v", targetIP, err)
		}


		result := &gnmi.UpdateResult{
			Path:      pathToUpdate[targetStr].GetPath(),
			Op:        gnmi.UpdateResult_UPDATE,
			Timestamp: timestamp,
		}
		results = append(results, result)
	}

	return &gnmi.SetResponse{
		Response:  results,
		Timestamp: timestamp,
	}, nil
}



func startTrapListener() {
	log.Println("Starting SNMP Trap listener on 0.0.0.0:162...")
	tl := gosnmp.NewTrapListener()
	tl.OnNewTrap = func(packet *gosnmp.SnmpPacket, addr *net.UDPAddr) {
		log.Printf("Received trap from %s", addr.IP.String())
		subManager.Publish(addr.IP.String(), packet)
	}
	if err := tl.Listen("0.0.0.0:162"); err != nil {
		log.Fatalf("Failed to start trap listener: %v", err)
	}
}
func snmpPacketToGnmiNotification(packet *gosnmp.SnmpPacket, prefix *gnmi.Path) *gnmi.Notification {
	updates := []*gnmi.Update{}
	for _, v := range packet.Variables {
		oidStr := strings.TrimPrefix(v.Name, ".")
		update := &gnmi.Update{
			Path: &gnmi.Path{
				Elem: []*gnmi.PathElem{{Name: oidStr}},
			},
			Val: snmpVarToGnmiTypedValue(v),
		}
		updates = append(updates, update)
	}

	return &gnmi.Notification{
		Timestamp: time.Now().UnixNano(),
		Prefix:    prefix,
		Update:    updates,
	}
}
func sendSnmpGet(targetIP, community string, version gosnmp.SnmpVersion, oids []string) (*gosnmp.SnmpPacket, error) {
	params := &gosnmp.GoSNMP{
		Target:    targetIP,
		Port:      161,
		Community: community,
		Version:   version,
		Timeout:   time.Duration(2) * time.Second,
	}
	if err := params.Connect(); err != nil {
		return nil, fmt.Errorf("SNMP connection error: %w", err)
	}
	defer params.Conn.Close()
	return params.Get(oids)
}
func gnmiPathToOidString(p *gnmi.Path) string {
	var parts []string
	for _, elem := range p.GetElem() {
		parts = append(parts, elem.GetName())
	}
	fullPath := strings.Join(parts, ".")
	return strings.Trim(fullPath, "/.")
}
func snmpVarToGnmiTypedValue(variable gosnmp.SnmpPDU) *gnmi.TypedValue {
	switch variable.Type {
	case gosnmp.OctetString:
		return &gnmi.TypedValue{Value: &gnmi.TypedValue_StringVal{StringVal: string(variable.Value.([]byte))}}
	case gosnmp.Integer, gosnmp.Counter32, gosnmp.Gauge32:
		return &gnmi.TypedValue{Value: &gnmi.TypedValue_IntVal{IntVal: int64(gosnmp.ToBigInt(variable.Value).Int64())}}
	case gosnmp.Counter64:
		return &gnmi.TypedValue{Value: &gnmi.TypedValue_UintVal{UintVal: gosnmp.ToBigInt(variable.Value).Uint64()}}
	case gosnmp.TimeTicks:
		return &gnmi.TypedValue{Value: &gnmi.TypedValue_UintVal{UintVal: uint64(variable.Value.(uint32))}}
	default:
		return &gnmi.TypedValue{Value: &gnmi.TypedValue_StringVal{StringVal: fmt.Sprintf("%v", variable.Value)}}
	}
}

func sendSnmpSet(targetIP, community string, version gosnmp.SnmpVersion, pdus []gosnmp.SnmpPDU) (*gosnmp.SnmpPacket, error) {
	params := &gosnmp.GoSNMP{
		Target:    targetIP,
		Port:      161,
		Community: community,
		Version:   version,
		Timeout:   time.Duration(5) * time.Second,
	}

	if err := params.Connect(); err != nil {
		return nil, fmt.Errorf("SNMP connection error: %w", err)
	}
	defer params.Conn.Close()

	result, err := params.Set(pdus)
	if err != nil {
		return nil, err
	}
	if result.Error != gosnmp.NoError {
		return nil, fmt.Errorf("SNMP Set error: %s", result.Error.String())
	}
	return result, nil
}


func gnmiTypedValueToSnmpPDU(oid string, val *gnmi.TypedValue) (*gosnmp.SnmpPDU, error) {
	pdu := &gosnmp.SnmpPDU{
		Name: "." + oid,
	}
	switch v := val.Value.(type) {



	case *gnmi.TypedValue_JsonVal:
		var strValue string

		if err := json.Unmarshal(v.JsonVal, &strValue); err != nil {
			return nil, fmt.Errorf("failed to unmarshal json_val: %w", err)
		}
		pdu.Type = gosnmp.OctetString
		pdu.Value = strValue
	case *gnmi.TypedValue_StringVal:
		pdu.Type = gosnmp.OctetString
		pdu.Value = v.StringVal


	default:
		return nil, fmt.Errorf("unsupported gNMI type for SNMP Set: %T", v)
	}
	return pdu, nil
}

func parseSnmpVersion(v string) gosnmp.SnmpVersion {
	switch strings.ToLower(v) {
	case "v1", "1":
		return gosnmp.Version1
	case "v3", "3":
		return gosnmp.Version3
	default:
		return gosnmp.Version2c
	}
}


func main() {
	go startTrapListener()
	listenAddr := "localhost:50051"
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	myServer := &server{}
	gnmi.RegisterGNMIServer(grpcServer, myServer)
	log.Printf("gNMI server listening on %s...", listenAddr)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
