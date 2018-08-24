/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lua

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/parnurzeal/gorequest"

	appsv1beta1 "k8s.io/api/apps/v1beta1"
	extensions "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"k8s.io/ingress-nginx/test/e2e/framework"
)

var _ = framework.IngressNginxDescribe("Dynamic Certificate", func() {
	f := framework.NewDefaultFramework("dynamic-certificate")
	host := "foo.com"

	BeforeEach(func() {
		err := enableDynamicCertificates(f.IngressController.Namespace, f.KubeClientSet)
		Expect(err).NotTo(HaveOccurred())

		err = f.WaitForNginxConfiguration(
			func(cfg string) bool {
				return strings.Contains(cfg, "ok, res = pcall(require, \"certificate\")")
			})
		Expect(err).NotTo(HaveOccurred())

		err = f.NewEchoDeploymentWithReplicas(1)
		Expect(err).NotTo(HaveOccurred())
	})

	It("picks up the certificate when we add TLS spec to existing ingress", func() {
		ing, err := f.EnsureIngress(framework.NewSingleIngress(host, "/", host, f.IngressController.Namespace, "http-svc", 80, nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(ing).NotTo(BeNil())
		time.Sleep(waitForLuaSync)
		resp, _, errs := gorequest.New().
			Get(f.IngressController.HTTPURL).
			Set("Host", host).
			End()
		Expect(len(errs)).Should(BeNumerically("==", 0))
		Expect(resp.StatusCode).Should(Equal(http.StatusOK))

		ing, err = f.KubeClientSet.ExtensionsV1beta1().Ingresses(f.IngressController.Namespace).Get("foo.com", metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		ing.Spec.TLS = []extensions.IngressTLS{
			{
				Hosts:      []string{host},
				SecretName: host,
			},
		}
		_, err = framework.CreateIngressTLSSecret(f.KubeClientSet,
			ing.Spec.TLS[0].Hosts,
			ing.Spec.TLS[0].SecretName,
			ing.Namespace)
		Expect(err).ToNot(HaveOccurred())
		_, err = f.KubeClientSet.ExtensionsV1beta1().Ingresses(f.IngressController.Namespace).Update(ing)
		Expect(err).ToNot(HaveOccurred())

		By("configuring HTTPS endpoint")
		err = f.WaitForNginxServer(host,
			func(server string) bool {
				return strings.Contains(server, "server_name "+host) &&
					strings.Contains(server, "listen 443")
			})
		Expect(err).ToNot(HaveOccurred())

		time.Sleep(waitForLuaSync)

		By("serving the configured certificate on HTTPS endpoint")
		resp, _, errs = gorequest.New().
			Get(f.IngressController.HTTPSURL).
			Set("Host", ing.Spec.TLS[0].Hosts[0]).
			TLSClientConfig(&tls.Config{
				InsecureSkipVerify: true,
				ServerName:         ing.Spec.TLS[0].Hosts[0],
			}).
			End()
		Expect(len(errs)).Should(BeNumerically("==", 0))
		Expect(resp.StatusCode).Should(Equal(http.StatusOK))
		Expect(len(resp.TLS.PeerCertificates)).Should(BeNumerically("==", 1))
		Expect(resp.TLS.PeerCertificates[0].DNSNames[0]).Should(Equal(host))
	})

	It("picks up the previously missing secret for a given ingress without reloading", func() {
		ing, err := f.EnsureIngress(framework.NewSingleIngressWithTLS(host, "/", host, f.IngressController.Namespace, "http-svc", 80, nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(ing).NotTo(BeNil())
		time.Sleep(waitForLuaSync)
		resp, _, errs := gorequest.New().
			Get(fmt.Sprintf("%s?id=dummy_log_splitter_foo_bar", f.IngressController.HTTPSURL)).
			Set("Host", host).
			TLSClientConfig(&tls.Config{
				InsecureSkipVerify: true,
				ServerName:         ing.Spec.TLS[0].Hosts[0],
			}).
			End()
		Expect(len(errs)).Should(BeNumerically("==", 0))
		Expect(resp.StatusCode).Should(Equal(http.StatusOK))

		_, err = framework.CreateIngressTLSSecret(f.KubeClientSet,
			ing.Spec.TLS[0].Hosts,
			ing.Spec.TLS[0].SecretName,
			ing.Namespace)
		Expect(err).ToNot(HaveOccurred())

		By("configuring certificate_by_lua and skipping Nginx configuration of the new certificate")
		err = f.WaitForNginxServer(host,
			func(server string) bool {
				return strings.Contains(server, "ssl_certificate_by_lua_block") &&
					!strings.Contains(server, fmt.Sprintf("ssl_certificate /etc/ingress-controller/ssl/%s-%s.pem;", ing.Namespace, host)) &&
					!strings.Contains(server, fmt.Sprintf("ssl_certificate_key /etc/ingress-controller/ssl/%s-%s.pem;", ing.Namespace, host)) &&
					strings.Contains(server, "listen 443")
			})
		Expect(err).ToNot(HaveOccurred())

		time.Sleep(waitForLuaSync)

		By("serving the configured certificate on HTTPS endpoint")
		resp, _, errs = gorequest.New().
			Get(f.IngressController.HTTPSURL).
			Set("Host", ing.Spec.TLS[0].Hosts[0]).
			TLSClientConfig(&tls.Config{
				InsecureSkipVerify: true,
				ServerName:         ing.Spec.TLS[0].Hosts[0],
			}).
			End()
		Expect(len(errs)).Should(BeNumerically("==", 0))
		Expect(resp.StatusCode).Should(Equal(http.StatusOK))
		Expect(len(resp.TLS.PeerCertificates)).Should(BeNumerically("==", 1))
		Expect(resp.TLS.PeerCertificates[0].DNSNames[0]).Should(Equal(host))

		log, err := f.NginxLogs()
		Expect(err).ToNot(HaveOccurred())
		Expect(log).ToNot(BeEmpty())
		index := strings.Index(log, "id=dummy_log_splitter_foo_bar")
		restOfLogs := log[index:]

		By("skipping Nginx reload")
		Expect(restOfLogs).ToNot(ContainSubstring(logRequireBackendReload))
		Expect(restOfLogs).ToNot(ContainSubstring(logBackendReloadSuccess))
		Expect(restOfLogs).To(ContainSubstring(logSkipBackendReload))
	})

	Context("given an ingress with TLS correctly configured", func() {
		BeforeEach(func() {
			ing, err := f.EnsureIngress(framework.NewSingleIngressWithTLS(host, "/", host, f.IngressController.Namespace, "http-svc", 80, nil))
			Expect(err).NotTo(HaveOccurred())
			Expect(ing).NotTo(BeNil())
			time.Sleep(waitForLuaSync)

			resp, _, errs := gorequest.New().
				Get(f.IngressController.HTTPSURL).
				Set("Host", host).
				TLSClientConfig(&tls.Config{
					InsecureSkipVerify: true,
					ServerName:         host,
				}).
				End()
			Expect(len(errs)).Should(BeNumerically("==", 0))
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))

			By("configuring HTTPS endpoint")
			err = f.WaitForNginxServer(host,
				func(server string) bool {
					return strings.Contains(server, "server_name "+host) &&
						strings.Contains(server, "listen 443")
				})
			Expect(err).ToNot(HaveOccurred())

			_, err = framework.CreateIngressTLSSecret(f.KubeClientSet,
				ing.Spec.TLS[0].Hosts,
				ing.Spec.TLS[0].SecretName,
				ing.Namespace)
			Expect(err).ToNot(HaveOccurred())
			time.Sleep(waitForLuaSync)

			By("configuring certificate_by_lua and skipping Nginx configuration of the new certificate")
			err = f.WaitForNginxServer(ing.Spec.TLS[0].Hosts[0],
				func(server string) bool {
					return strings.Contains(server, "ssl_certificate_by_lua_block") &&
						!strings.Contains(server, fmt.Sprintf("ssl_certificate /etc/ingress-controller/ssl/%s-%s.pem;", ing.Namespace, host)) &&
						!strings.Contains(server, fmt.Sprintf("ssl_certificate_key /etc/ingress-controller/ssl/%s-%s.pem;", ing.Namespace, host)) &&
						strings.Contains(server, "listen 443")
				})
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(waitForLuaSync)

			By("serving the configured certificate on HTTPS endpoint")
			resp, _, errs = gorequest.New().
				Get(f.IngressController.HTTPSURL).
				Set("Host", ing.Spec.TLS[0].Hosts[0]).
				TLSClientConfig(&tls.Config{
					InsecureSkipVerify: true,
					ServerName:         ing.Spec.TLS[0].Hosts[0],
				}).
				End()
			Expect(len(errs)).Should(BeNumerically("==", 0))
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))
			Expect(len(resp.TLS.PeerCertificates)).Should(BeNumerically("==", 1))
			Expect(resp.TLS.PeerCertificates[0].DNSNames[0]).Should(Equal(host))
		})

		It("picks up the updated certificate without reloading", func() {
			ing, err := f.KubeClientSet.ExtensionsV1beta1().Ingresses(f.IngressController.Namespace).Get("foo.com", metav1.GetOptions{})

			resp, _, errs := gorequest.New().
				Get(fmt.Sprintf("%s?id=dummy_log_splitter_foo_bar", f.IngressController.HTTPSURL)).
				Set("Host", host).
				TLSClientConfig(&tls.Config{
					InsecureSkipVerify: true,
					ServerName:         ing.Spec.TLS[0].Hosts[0],
				}).
				End()
			Expect(len(errs)).Should(BeNumerically("==", 0))
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))

			_, err = framework.CreateIngressTLSSecret(f.KubeClientSet,
				ing.Spec.TLS[0].Hosts,
				ing.Spec.TLS[0].SecretName,
				ing.Namespace)
			Expect(err).ToNot(HaveOccurred())
			time.Sleep(waitForLuaSync)

			By("configuring certificate_by_lua and skipping Nginx configuration of the new certificate")
			err = f.WaitForNginxServer(ing.Spec.TLS[0].Hosts[0],
				func(server string) bool {
					return strings.Contains(server, "ssl_certificate_by_lua_block") &&
						!strings.Contains(server, fmt.Sprintf("ssl_certificate /etc/ingress-controller/ssl/%s-%s.pem;", ing.Namespace, host)) &&
						!strings.Contains(server, fmt.Sprintf("ssl_certificate_key /etc/ingress-controller/ssl/%s-%s.pem;", ing.Namespace, host)) &&
						strings.Contains(server, "listen 443")
				})
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(waitForLuaSync)

			By("serving the configured certificate on HTTPS endpoint")
			resp, _, errs = gorequest.New().
				Get(f.IngressController.HTTPSURL).
				Set("Host", ing.Spec.TLS[0].Hosts[0]).
				TLSClientConfig(&tls.Config{
					InsecureSkipVerify: true,
					ServerName:         ing.Spec.TLS[0].Hosts[0],
				}).
				End()
			Expect(len(errs)).Should(BeNumerically("==", 0))
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))
			Expect(len(resp.TLS.PeerCertificates)).Should(BeNumerically("==", 1))
			Expect(resp.TLS.PeerCertificates[0].DNSNames[0]).Should(Equal(host))

			log, err := f.NginxLogs()
			Expect(err).ToNot(HaveOccurred())
			Expect(log).ToNot(BeEmpty())
			index := strings.Index(log, "id=dummy_log_splitter_foo_bar")
			restOfLogs := log[index:]

			By("skipping Nginx reload")
			Expect(restOfLogs).ToNot(ContainSubstring(logRequireBackendReload))
			Expect(restOfLogs).ToNot(ContainSubstring(logBackendReloadSuccess))
			Expect(restOfLogs).To(ContainSubstring(logSkipBackendReload))
		})

		It("falls back to using default certificate when secret gets deleted without reloading", func() {
			ing, err := f.KubeClientSet.ExtensionsV1beta1().Ingresses(f.IngressController.Namespace).Get("foo.com", metav1.GetOptions{})

			resp, _, errs := gorequest.New().
				Get(fmt.Sprintf("%s?id=dummy_log_splitter_foo_bar", f.IngressController.HTTPSURL)).
				Set("Host", host).
				TLSClientConfig(&tls.Config{
					InsecureSkipVerify: true,
					ServerName:         ing.Spec.TLS[0].Hosts[0],
				}).
				End()
			Expect(len(errs)).Should(BeNumerically("==", 0))
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))

			f.KubeClientSet.CoreV1().Secrets(ing.Namespace).Delete(ing.Spec.TLS[0].SecretName, nil)
			Expect(err).ToNot(HaveOccurred())
			time.Sleep(waitForLuaSync)

			By("configuring certificate_by_lua and skipping Nginx configuration of the new certificate")
			err = f.WaitForNginxServer(ing.Spec.TLS[0].Hosts[0],
				func(server string) bool {
					return strings.Contains(server, "ssl_certificate_by_lua_block") &&
						strings.Contains(server, "ssl_certificate /etc/ingress-controller/ssl/default-fake-certificate.pem;") &&
						strings.Contains(server, "ssl_certificate_key /etc/ingress-controller/ssl/default-fake-certificate.pem;") &&
						strings.Contains(server, "listen 443")
				})
			Expect(err).ToNot(HaveOccurred())

			time.Sleep(waitForLuaSync)

			By("serving the default certificate on HTTPS endpoint")
			resp, _, errs = gorequest.New().
				Get(f.IngressController.HTTPSURL).
				Set("Host", ing.Spec.TLS[0].Hosts[0]).
				TLSClientConfig(&tls.Config{
					InsecureSkipVerify: true,
					ServerName:         ing.Spec.TLS[0].Hosts[0],
				}).
				End()
			Expect(len(errs)).Should(BeNumerically("==", 0))
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))
			Expect(len(resp.TLS.PeerCertificates)).Should(BeNumerically("==", 1))
			Expect(resp.TLS.PeerCertificates[0].Issuer.CommonName).Should(Equal("Kubernetes Ingress Controller Fake Certificate"))

			log, err := f.NginxLogs()
			Expect(err).ToNot(HaveOccurred())
			Expect(log).ToNot(BeEmpty())
			index := strings.Index(log, "id=dummy_log_splitter_foo_bar")
			restOfLogs := log[index:]

			By("skipping Nginx reload")
			Expect(restOfLogs).ToNot(ContainSubstring(logRequireBackendReload))
			Expect(restOfLogs).ToNot(ContainSubstring(logBackendReloadSuccess))
			Expect(restOfLogs).To(ContainSubstring(logSkipBackendReload))
		})

		It("picks up a non-certificate only change", func() {
			newHost := "foo2.com"
			ing, err := f.KubeClientSet.ExtensionsV1beta1().Ingresses(f.IngressController.Namespace).Get("foo.com", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			ing.Spec.Rules[0].Host = newHost
			_, err = f.KubeClientSet.ExtensionsV1beta1().Ingresses(f.IngressController.Namespace).Update(ing)
			Expect(err).ToNot(HaveOccurred())

			By("configuring HTTPS endpoint")
			err = f.WaitForNginxServer(newHost,
				func(server string) bool {
					return strings.Contains(server, "server_name "+newHost) &&
						strings.Contains(server, "listen 443")
				})
			Expect(err).ToNot(HaveOccurred())

			By("serving the configured certificate on HTTPS endpoint")
			resp, _, errs := gorequest.New().
				Get(f.IngressController.HTTPSURL).
				Set("Host", newHost).
				TLSClientConfig(&tls.Config{
					InsecureSkipVerify: true,
					ServerName:         newHost,
				}).
				End()
			Expect(len(errs)).Should(BeNumerically("==", 0))
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))
			Expect(len(resp.TLS.PeerCertificates)).Should(BeNumerically("==", 1))
			Expect(resp.TLS.PeerCertificates[0].Issuer.CommonName).Should(Equal("Kubernetes Ingress Controller Fake Certificate"))
		})

		It("removes HTTPS configuration when we delete TLS spec", func() {
			ing, err := f.KubeClientSet.ExtensionsV1beta1().Ingresses(f.IngressController.Namespace).Get("foo.com", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			ing.Spec.TLS = []extensions.IngressTLS{}

			_, err = f.KubeClientSet.ExtensionsV1beta1().Ingresses(f.IngressController.Namespace).Update(ing)
			Expect(err).ToNot(HaveOccurred())
			By("configuring HTTP endpoint")
			err = f.WaitForNginxServer(host,
				func(server string) bool {
					return !strings.Contains(server, "ssl_certificate_by_lua_block") &&
						!strings.Contains(server, "listen 443")
				})
			Expect(err).ToNot(HaveOccurred())

			resp, _, errs := gorequest.New().
				Get(f.IngressController.HTTPURL).
				Set("Host", host).
				End()
			Expect(len(errs)).Should(BeNumerically("==", 0))
			Expect(resp.StatusCode).Should(Equal(http.StatusOK))
		})
	})
})

func enableDynamicCertificates(namespace string, kubeClientSet kubernetes.Interface) error {
	return framework.UpdateDeployment(kubeClientSet, namespace, "nginx-ingress-controller", 1,
		func(deployment *appsv1beta1.Deployment) error {
			args := deployment.Spec.Template.Spec.Containers[0].Args
			args = append(args, "--enable-dynamic-certificates")
			args = append(args, "--enable-ssl-chain-completion=false")
			deployment.Spec.Template.Spec.Containers[0].Args = args
			_, err := kubeClientSet.AppsV1beta1().Deployments(namespace).Update(deployment)

			return err
		})
}