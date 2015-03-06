FROM crosbymichael/golang
MAINTAINER Miek Gieben <miek@miek.nl> (@miekg)

ADD . /go/src/github.com/erimatnor/skydns-kubernetes
RUN go get github.com/erimatnor/skydns-kubernetes

EXPOSE 53
ENTRYPOINT ["skydns"]
