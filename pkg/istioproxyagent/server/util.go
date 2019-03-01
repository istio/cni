package server

import (
	"encoding/json"
	"io/ioutil"
	"math/rand"
	"os"
	"strconv"
	"time"
)

func toJSON(obj interface{}) (string, error) {
	bytes, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func createVolumes() (string, string, error) {
	random := rand.New(rand.NewSource(time.Now().UnixNano()))
	dir := "/tmp/istio-proxy-volumes-" + strconv.Itoa(random.Int())
	certsDir := dir + "/certs"
	err := os.MkdirAll(certsDir, os.ModePerm)
	if err != nil {
		return "", "", err
	}

	confDir := dir + "/conf"
	err = os.Mkdir(confDir, os.ModePerm)
	if err != nil {
		return "", "", err
	}

	// ensure the conf dir is world writable (might not be if umask is set)
	err = os.Chmod(confDir, 0777)
	if err != nil {
		return "", "", err
	}

	return certsDir, confDir, nil
}

func writeSecret(dir string, secretData map[string][]byte) error {
	for k, v := range secretData {
		err := ioutil.WriteFile(dir+"/"+k, v, os.ModePerm)
		if err != nil {
			return err
		}
	}
	return nil
}
