package kubernetes

import (
	"testing"

	"github.com/docker/compose-on-kubernetes/api/labels"
	"github.com/docker/docker/api/types/swarm"
	"gotest.tools/assert"
	appsv1beta2 "k8s.io/api/apps/v1beta2"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimachineryTypes "k8s.io/apimachinery/pkg/types"
	apimachineryUtil "k8s.io/apimachinery/pkg/util/intstr"
)

func TestReplicasConversionNeedsAService(t *testing.T) {
	replicas := appsv1beta2.ReplicaSetList{
		Items: []appsv1beta2.ReplicaSet{makeReplicaSet("unknown", 0, 0)},
	}
	services := apiv1.ServiceList{}
	_, err := convertToServices(&replicas, &appsv1beta2.DaemonSetList{}, &services)
	assert.ErrorContains(t, err, "could not find service")
}

func TestKubernetesServiceToSwarmServiceConversion(t *testing.T) {
	testCases := []struct {
		doc              string
		replicas         *appsv1beta2.ReplicaSetList
		services         *apiv1.ServiceList
		expectedServices []swarm.Service
	}{
		{
			doc: "Match replicas with headless stack services",
			replicas: &appsv1beta2.ReplicaSetList{
				Items: []appsv1beta2.ReplicaSet{
					makeReplicaSet("service1", 2, 5),
					makeReplicaSet("service2", 3, 3),
				},
			},
			services: &apiv1.ServiceList{
				Items: []apiv1.Service{
					makeKubeService("service1", "stack", "uid1", apiv1.ServiceTypeClusterIP, nil),
					makeKubeService("service2", "stack", "uid2", apiv1.ServiceTypeClusterIP, nil),
					makeKubeService("service3", "other-stack", "uid2", apiv1.ServiceTypeClusterIP, nil),
				},
			},
			expectedServices: []swarm.Service{
				makeSwarmService(t, "stack_service1", "uid1", withMode("replicated", 5), withStatus(2, 5)),
				makeSwarmService(t, "stack_service2", "uid2", withMode("replicated", 3), withStatus(3, 3)),
			},
		},
		{
			doc: "Headless service and LoadBalancer Service are tied to the same Swarm service",
			replicas: &appsv1beta2.ReplicaSetList{
				Items: []appsv1beta2.ReplicaSet{
					makeReplicaSet("service", 1, 1),
				},
			},
			services: &apiv1.ServiceList{
				Items: []apiv1.Service{
					makeKubeService("service", "stack", "uid1", apiv1.ServiceTypeClusterIP, nil),
					makeKubeService("service-published", "stack", "uid2", apiv1.ServiceTypeLoadBalancer, []apiv1.ServicePort{
						{
							Port:       80,
							TargetPort: apimachineryUtil.FromInt(80),
							Protocol:   apiv1.ProtocolTCP,
						},
					}),
				},
			},
			expectedServices: []swarm.Service{
				makeSwarmService(t, "stack_service", "uid1",
					withMode("replicated", 1),
					withStatus(1, 1), withPort(swarm.PortConfig{
						PublishMode:   swarm.PortConfigPublishModeIngress,
						PublishedPort: 80,
						TargetPort:    80,
						Protocol:      swarm.PortConfigProtocolTCP,
					}),
				),
			},
		},
		{
			doc: "Headless service and NodePort Service are tied to the same Swarm service",
			replicas: &appsv1beta2.ReplicaSetList{
				Items: []appsv1beta2.ReplicaSet{
					makeReplicaSet("service", 1, 1),
				},
			},
			services: &apiv1.ServiceList{
				Items: []apiv1.Service{
					makeKubeService("service", "stack", "uid1", apiv1.ServiceTypeClusterIP, nil),
					makeKubeService("service-random-ports", "stack", "uid2", apiv1.ServiceTypeNodePort, []apiv1.ServicePort{
						{
							Port:       35666,
							TargetPort: apimachineryUtil.FromInt(80),
							Protocol:   apiv1.ProtocolTCP,
						},
					}),
				},
			},
			expectedServices: []swarm.Service{
				makeSwarmService(t, "stack_service", "uid1",
					withMode("replicated", 1),
					withStatus(1, 1),
					withPort(swarm.PortConfig{
						PublishMode:   swarm.PortConfigPublishModeHost,
						PublishedPort: 35666,
						TargetPort:    80,
						Protocol:      swarm.PortConfigProtocolTCP,
					}),
				),
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.doc, func(t *testing.T) {
			swarmServices, err := convertToServices(tc.replicas, &appsv1beta2.DaemonSetList{}, tc.services)
			assert.NilError(t, err)
			assert.DeepEqual(t, tc.expectedServices, swarmServices)
		})
	}
}

func makeReplicaSet(service string, available, replicas int32) appsv1beta2.ReplicaSet {
	return appsv1beta2.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				labels.ForServiceName: service,
			},
		},
		Spec: appsv1beta2.ReplicaSetSpec{
			Template: apiv1.PodTemplateSpec{
				Spec: apiv1.PodSpec{
					Containers: []apiv1.Container{
						{
							Image: "image",
						},
					},
				},
			},
		},
		Status: appsv1beta2.ReplicaSetStatus{
			AvailableReplicas: available,
			Replicas:          replicas,
		},
	}
}

func makeKubeService(service, stack, uid string, serviceType apiv1.ServiceType, ports []apiv1.ServicePort) apiv1.Service {
	return apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				labels.ForStackName: stack,
			},
			Name: service,
			UID:  apimachineryTypes.UID(uid),
		},
		Spec: apiv1.ServiceSpec{
			Type:  serviceType,
			Ports: ports,
		},
	}
}

func withMode(mode string, replicas uint64) func(*swarm.Service) {
	return func(service *swarm.Service) {
		switch mode {
		case "global":
			service.Spec.Mode = swarm.ServiceMode{
				Global: &swarm.GlobalService{},
			}
		case "replicated":
			service.Spec.Mode = swarm.ServiceMode{
				Replicated: &swarm.ReplicatedService{Replicas: &replicas},
			}
			withStatus(0, replicas)
		default:
			service.Spec.Mode = swarm.ServiceMode{}
			withStatus(0, 0)
		}
	}
}

func withPort(port swarm.PortConfig) func(*swarm.Service) {
	return func(service *swarm.Service) {
		if service.Endpoint.Ports == nil {
			service.Endpoint.Ports = make([]swarm.PortConfig, 0)
		}
		service.Endpoint.Ports = append(service.Endpoint.Ports, port)
	}
}

func withStatus(running, desired uint64) func(*swarm.Service) {
	return func(service *swarm.Service) {
		service.ServiceStatus = &swarm.ServiceStatus{
			RunningTasks: running,
			DesiredTasks: desired,
		}
	}
}

func makeSwarmService(t *testing.T, service, id string, opts ...func(*swarm.Service)) swarm.Service {
	t.Helper()
	s := swarm.Service{
		ID: id,
		Spec: swarm.ServiceSpec{
			Annotations: swarm.Annotations{
				Name: service,
			},
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{
					Image: "image",
				},
			},
		},
	}
	for _, o := range opts {
		o(&s)
	}
	return s
}
