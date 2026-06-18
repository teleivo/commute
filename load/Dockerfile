FROM alpine:3
RUN apk add --no-cache bind-tools

ARG VEGETA_VERSION=12.13.0
ARG VEGETA_SHA256=e8759ce45c14e18374bdccd3ba6068197bc3a9f9b7e484db3837f701b9d12e61
RUN wget -qO /tmp/vegeta.tar.gz \
        "https://github.com/tsenart/vegeta/releases/download/v${VEGETA_VERSION}/vegeta_${VEGETA_VERSION}_linux_amd64.tar.gz" \
    && echo "${VEGETA_SHA256}  /tmp/vegeta.tar.gz" | sha256sum -c \
    && tar --extract --gzip --file=/tmp/vegeta.tar.gz vegeta \
    && mv vegeta /usr/local/bin/vegeta \
    && rm /tmp/vegeta.tar.gz

COPY load/load.sh /bin/load.sh
COPY load/load-local.sh /bin/load-local.sh
RUN chmod +x /bin/load.sh /bin/load-local.sh
ENTRYPOINT ["/bin/load.sh"]
