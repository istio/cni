module istio.io/cni

go 1.13

require (
	github.com/containernetworking/cni v0.7.0-alpha1
	github.com/containernetworking/plugins v0.7.3
	github.com/patrickmn/go-cache v2.1.0+incompatible
	github.com/spf13/pflag v1.0.5
	github.com/spf13/viper v1.4.0
	go.uber.org/zap v1.10.0
	istio.io/api v0.0.0-20191113030652-62bf0afefa2f
	istio.io/pkg v0.0.0-20191113122952-4f521de9c8ca
	k8s.io/api v0.0.0-20191025225708-5524a3672fbb
	k8s.io/apimachinery v0.0.0-20191025225532-af6325b3a843
	k8s.io/client-go v0.0.0-20191016111102-bec269661e48
	k8s.io/utils v0.0.0-20191010214722-8d271d903fe4 // indirect
	sigs.k8s.io/controller-runtime v0.4.0
)
