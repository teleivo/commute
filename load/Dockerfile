FROM alpine:3
RUN apk add --no-cache bind-tools curl
COPY load/load.sh /bin/load.sh
# load-local.sh is used by the local docker-compose setup (entrypoint override); not used on Fly
COPY load/load-local.sh /bin/load-local.sh
RUN chmod +x /bin/load.sh /bin/load-local.sh
ENTRYPOINT ["/bin/load.sh"]
