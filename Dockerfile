FROM quay.io/prometheus/busybox:latest

COPY loadtest /bin/loadtest
ENTRYPOINT ["/bin/loadtest"]
