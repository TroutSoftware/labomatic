# EIP over two sites

site1 = Subnet(link_only=True)
site2 = Subnet(link_only=True)

r1 = Router("mrt")
r1.attach_nic(site1)
r1.attach_nic(site2)

# 10.10.10.10
d1 = Router("sensor1")
d1.attach_nic(site1)

# 10.10.10.11
d2 = Router("sensor2")
d2.attach_nic(site1)

# 10.10.10.20
ctrl = Router("ctrl")
ctrl.attach_nic(site2)