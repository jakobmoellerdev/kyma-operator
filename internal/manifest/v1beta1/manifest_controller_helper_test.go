package v1beta1_test

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/kyma-project/lifecycle-manager/api/v1beta1"
	internalv1beta1 "github.com/kyma-project/lifecycle-manager/internal/manifest/v1beta1"
	_ "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/kyma-project/lifecycle-manager/pkg/labels"
)

type mockLayer struct {
	filePath string
}

func (m mockLayer) Digest() (v1.Hash, error) {
	r, err := m.Compressed()
	if err != nil {
		return v1.Hash{}, err
	}
	defer r.Close()
	hash, _, err := v1.SHA256(r)
	return hash, err
}

func (m mockLayer) MediaType() (types.MediaType, error) {
	return types.OCILayer, nil
}

func (m mockLayer) Size() (int64, error) { return 137438691328, nil }
func (m mockLayer) Compressed() (io.ReadCloser, error) {
	f, err := os.Open(m.filePath)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(f), nil
}

func (m mockLayer) Uncompressed() (io.ReadCloser, error) {
	f, err := os.Open("../../../pkg/test_samples/oci/config.yaml")
	if err != nil {
		return nil, err
	}
	return io.NopCloser(f), nil
}

func (m mockLayer) DiffID() (v1.Hash, error) {
	return v1.Hash{Algorithm: "fake", Hex: "diff id"}, nil
}

func CreateImageSpecLayer(ociLayerType OCILayerType) v1.Layer {
	var layer v1.Layer
	var err error
	if ociLayerType == layerCRDs {
		layer, err = partial.CompressedToLayer(mockLayer{filePath: "../../../pkg/test_samples/oci/crd.tgz"})
	} else {
		layer, err = partial.CompressedToLayer(mockLayer{filePath: "../../../pkg/test_samples/oci/helm_chart_with_crds.tgz"})
	}
	Expect(err).ToNot(HaveOccurred())
	return layer
}

type OCILayerType int

// Valid Helm States.
const (
	layerCRDs OCILayerType = iota
	layerInstalls
)

func PushToRemoteOCIRegistry(layerName string, ociLayerType OCILayerType) {
	layer := CreateImageSpecLayer(ociLayerType)
	digest, err := layer.Digest()
	Expect(err).ToNot(HaveOccurred())

	// Set up a fake registry and write what we pulled to it.
	u, err := url.Parse(server.URL)
	Expect(err).NotTo(HaveOccurred())

	dst := fmt.Sprintf("%s/%s@%s", u.Host, layerName, digest)
	ref, err := name.NewDigest(dst)
	Expect(err).ToNot(HaveOccurred())

	err = remote.WriteLayer(ref.Context(), layer)
	Expect(err).ToNot(HaveOccurred())

	got, err := remote.Layer(ref)
	Expect(err).ToNot(HaveOccurred())
	gotHash, err := got.Digest()
	Expect(err).ToNot(HaveOccurred())
	Expect(gotHash).To(Equal(digest))
}

//nolint:unparam
func createOCIImageSpec(name, repo string, ociLayerType OCILayerType) v1beta1.ImageSpec {
	imageSpec := v1beta1.ImageSpec{
		Name: name,
		Repo: repo,
		Type: "oci-ref",
	}
	layer := CreateImageSpecLayer(ociLayerType)
	digest, err := layer.Digest()
	Expect(err).ToNot(HaveOccurred())
	imageSpec.Ref = digest.String()
	return imageSpec
}

func NewTestManifest(prefix string) *v1beta1.Manifest {
	return &v1beta1.Manifest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", prefix, rand.Intn(999999)),
			Namespace: metav1.NamespaceDefault,
			Labels: map[string]string{
				labels.KymaName: string(uuid.NewUUID()),
			},
		},
	}
}

func deleteHelmChartResources(imageSpec v1beta1.ImageSpec) {
	chartYamlPath := filepath.Join(internalv1beta1.GetFsChartPath(imageSpec), "Chart.yaml")
	Expect(os.RemoveAll(chartYamlPath)).Should(Succeed())
	valuesYamlPath := filepath.Join(internalv1beta1.GetFsChartPath(imageSpec), "values.yaml")
	Expect(os.RemoveAll(valuesYamlPath)).Should(Succeed())
	templatesPath := filepath.Join(internalv1beta1.GetFsChartPath(imageSpec), "templates")
	Expect(os.RemoveAll(templatesPath)).Should(Succeed())
}

func verifyHelmResourcesDeletion(imageSpec v1beta1.ImageSpec) {
	_, err := os.Stat(filepath.Join(internalv1beta1.GetFsChartPath(imageSpec), "Chart.yaml"))
	Expect(os.IsNotExist(err)).To(BeTrue())
	_, err = os.Stat(filepath.Join(internalv1beta1.GetFsChartPath(imageSpec), "values.yaml"))
	Expect(os.IsNotExist(err)).To(BeTrue())
	_, err = os.Stat(filepath.Join(internalv1beta1.GetFsChartPath(imageSpec), "templates"))
	Expect(os.IsNotExist(err)).To(BeTrue())
}
