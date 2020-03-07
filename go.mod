module istio.io/cni

go 1.13

replace istio.io/api => github.com/ChenLingPeng/istioapi v0.0.0-20200307063149-1b3e8d33b531

require (
	github.com/containernetworking/cni v0.7.0-alpha1
	github.com/containernetworking/plugins v0.7.3
	github.com/evanphx/json-patch v4.5.0+incompatible // indirect
	github.com/googleapis/gnostic v0.3.1 // indirect
	github.com/imdario/mergo v0.3.6 // indirect
	github.com/spf13/pflag v1.0.5
	github.com/spf13/viper v1.4.0
	go.uber.org/multierr v1.1.0
	go.uber.org/zap v1.10.0
	gopkg.in/check.v1 v1.0.0-20190902080502-41f04d3bba15 // indirect
	istio.io/api v0.0.0-20190515205759-982e5c3888c6
	istio.io/pkg v0.0.0-20191113122952-4f521de9c8ca
	k8s.io/api v0.17.0
	k8s.io/apimachinery v0.17.0
	k8s.io/client-go v0.0.0-20191016111102-bec269661e48
	k8s.io/utils v0.0.0-20191010214722-8d271d903fe4 // indirect
)
