#!/bin/bash -x
version=3.5.4
arch=amd64

mkdir -p $GOPATH/bin
curl -L -O "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv${version}/kustomize_v${version}_linux_${arch}.tar.gz"
tar xvf kustomize_v${version}_linux_${arch}.tar.gz
mv kustomize $GOPATH/bin
