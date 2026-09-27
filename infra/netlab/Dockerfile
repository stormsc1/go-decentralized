# Helper for the network lab in compose.yaml: NAT routers and LAN hosts.
FROM alpine:3.20
RUN apk add --no-cache iptables
COPY router.sh host.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/router.sh /usr/local/bin/host.sh
