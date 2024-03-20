# Simple router + 2 hosts lab

datanet = Subnet(network="172.16.0.0/20")
hostnet = Subnet(network="172.16.16.0/24", host=True)

r1 = Router("main-router")
r1.attach_nic(datanet, addr=datanet.addr(1))
r1.attach_nic(hostnet, addr=hostnet.addr(2))


#d1 = Host()
#d1.attach_nic(datanet)
#d1.attach_nic(hostnet)

# d2 = Host("h2", image="alpine")
# d2.attach_nic(datanet)