FROM alpine:3.20
RUN apk add --no-cache bash curl ca-certificates tmux && \
    wget -qO /usr/local/bin/kubectl "https://dl.k8s.io/release/v1.31.4/bin/linux/amd64/kubectl" && \
    chmod +x /usr/local/bin/kubectl && \
    wget -qO /tmp/k9s.tar.gz "https://github.com/derailed/k9s/releases/download/v0.32.7/k9s_Linux_amd64.tar.gz" && \
    tar -xzf /tmp/k9s.tar.gz -C /usr/local/bin k9s && rm /tmp/k9s.tar.gz && \
    wget -qO /usr/local/bin/ttyd "https://github.com/tsl0922/ttyd/releases/download/1.7.7/ttyd.x86_64" && \
    chmod +x /usr/local/bin/ttyd
EXPOSE 7681
CMD ["ttyd", "--writable", "bash"]
