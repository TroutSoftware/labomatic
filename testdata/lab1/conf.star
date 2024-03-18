# Simple router + 2 hosts lab

datanet = Subnet(network="172.16.0.0/20")
# mgnet = Subnet("net999", tag=999, network="172.16.0.0/20")

r = Router("r1")
r.attach_nic(datanet, addr=datanet.addr(1))
# r.attach_nic(mgnet, addr=mgnet.addr(1))


d1 = Host()
d1.attach_nic(datanet, name="eno1")

# d2 = Host("h2", image="alpine")
# d2.attach_nic(datanet)