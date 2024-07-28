# Simple router + 2 hosts lab

btx1 = Subnet(network="10.10.0.0/16")
btx2 = Subnet(network="10.10.0.0/16", host=True)
itco = Subnet(network="198.10.1.0/24")

internet = Outnet()

r1 = Router("mrt")
r1.attach_nic(datanet, addr=datanet.addr(1))
r1.attach_nic(hostnet, addr=hostnet.addr(2))

r1.init_script = """
/ip/pool
add ranges=172.16.0.10-172.16.0.20

/ip/dhcp-server/network
add address=172.16.0/24 gateway=172.16.0.1 dns-server=172.16.0.1

/ip/dhcp-server
add address-pool=pool0 interface=ether2
"""

r2 = Router("pfs2")
r2.attach_nic(btx2, addr=btx2.addr(1))
r2.attach_nic(itco, addr=itco.addr(2))
r2.init_script = """
/ip/address/add address=100.65.0.1/16 interface=ether2
/ip/route/add gateway=198.10.1.1 dst-address=100.64.0.0/16

d1 = Router("other")
