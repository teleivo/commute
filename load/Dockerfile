FROM alpine:3
RUN apk add --no-cache bind-tools curl
COPY load/load.sh /bin/load.sh
RUN chmod +x /bin/load.sh
ENTRYPOINT ["/bin/load.sh"]
