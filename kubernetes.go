package main

import (
	"flag"
	"log"
	"net"
	"sync"
	"time"
	"fmt"
	
	"encoding/json"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	pconfig "github.com/GoogleCloudPlatform/kubernetes/pkg/proxy/config"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	"github.com/coreos/go-etcd/etcd"
	"github.com/skynetservices/skydns/msg"
)

// The periodic interval for checking the state of things.
const syncInterval = 5 * time.Second

type KubernetesSync struct {
	mu         sync.Mutex // protects serviceMap
	serviceMap map[string]*serviceInfo
	eclient    *etcd.Client
}

func NewKubernetesSync(client *etcd.Client) *KubernetesSync {
	ks := &KubernetesSync{
		serviceMap: make(map[string]*serviceInfo),
		eclient:    client,
	}
	return ks
}

func buildNameString(service, namespace, domain string) string {
	return fmt.Sprintf("%s.%s.%s.", service, namespace, domain)
}

// OnUpdate manages the active set of service records.
// Active service records get ttl bumps if found in the update set or
// removed if missing from the update set.
func (ksync *KubernetesSync) OnUpdate(services []api.Service) {
	activeServices := util.StringSet{}
	for _, service := range services {
		activeServices.Insert(service.Name)
		info, exists := ksync.getServiceInfo(service.ObjectMeta.Name)
		serviceIP := net.ParseIP(service.Spec.PortalIP)

		name := buildNameString(service.ObjectMeta.Name, service.ObjectMeta.Namespace, config.Domain)

		if exists && (info.portalPort != service.Spec.Port || !info.portalIP.Equal(serviceIP)) {
			err := ksync.removeDNS(name, info)
			if err != nil {
				log.Printf("failed to remove dns for %q: %s\n", name, err)
			}
		}
		log.Printf("adding new service %q at %s:%d/%s (local :%d)\n", name, serviceIP, service.Spec.Port, service.Spec.Protocol, service.Spec.Port)
		si := &serviceInfo{
			Port: service.Spec.Port,
			protocol:  service.Spec.Protocol,
			active:    true,
		}
		ksync.setServiceInfo(service.ObjectMeta.Name, si)
		si.portalIP = serviceIP
		si.portalPort = service.Spec.Port
		err := ksync.addDNS(name, si)
		if err != nil {
			log.Println("failed to add dns %q: %s", name, err)
		}
	}
	ksync.mu.Lock()
	defer ksync.mu.Unlock()
	for name, info := range ksync.serviceMap {
		if !activeServices.Has(name) {
			err := ksync.removeDNS(name, info)
			if err != nil {
				log.Println("failed to remove dns for %q: %s", name, err)
			}
			delete(ksync.serviceMap, name)
		}
	}
}

func (ksync *KubernetesSync) getServiceInfo(service string) (*serviceInfo, bool) {
	ksync.mu.Lock()
	defer ksync.mu.Unlock()
	info, ok := ksync.serviceMap[service]
	return info, ok
}

func (ksync *KubernetesSync) setServiceInfo(service string, info *serviceInfo) {
	ksync.mu.Lock()
	defer ksync.mu.Unlock()
	ksync.serviceMap[service] = info
}

func (ksync *KubernetesSync) removeDNS(service string, info *serviceInfo) error {
	// Remove from SkyDNS registration
	log.Printf("removing %s from DNS", service)
	_, err := ksync.eclient.Delete(msg.Path(service), true)
	return err
}

func (ksync *KubernetesSync) addDNS(service string, info *serviceInfo) error {
	// ADD to SkyDNS registry
	svc := msg.Service{
		Host:     info.portalIP.String(),
		Port:     info.portalPort,
		Priority: 10,
		Weight:   10,
		Ttl:      30,
	}
	b, err := json.Marshal(svc)

	//Set with no TTL, and hope that kubernetes events are accurate.

	log.Printf("setting dns record: %v\n", service)
	_, err = ksync.eclient.Set(msg.Path(service), string(b), uint64(0))
	return err
}

type serviceInfo struct {
	portalIP   net.IP
	portalPort int
	protocol   api.Protocol
	Port  int
	mu         sync.Mutex // protects active
	active     bool
}

func init() {
	client.BindClientConfigFlags(flag.CommandLine, clientConfig)
}

func WatchKubernetes(eclient *etcd.Client) {
	serviceConfig := pconfig.NewServiceConfig()
	endpointsConfig := pconfig.NewEndpointsConfig()

	if clientConfig.Host != "" {
		log.Printf("using api calls to get Kubernetes config %v\n", clientConfig.Host)
		client, err := client.New(clientConfig)
		if err != nil {
			log.Fatalf("Kubernetes requested, but received invalid API configuration: %v", err)
		}
		pconfig.NewSourceAPI(
			client.Services(api.NamespaceAll),
			client.Endpoints(api.NamespaceAll),
			syncInterval,
			serviceConfig.Channel("api"),
			endpointsConfig.Channel("api"),
		)
	}

	ks := NewKubernetesSync(eclient)
	// Wire skydns to handle changes to services
	serviceConfig.RegisterHandler(ks)
}
