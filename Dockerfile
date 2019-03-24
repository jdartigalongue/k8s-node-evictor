FROM alpine:latest

RUN mkdir -p /app
COPY ./build/k8s-node-evictor /app/k8s-node-evictor

WORKDIR /app

CMD ["/app/k8s-node-evictor"]