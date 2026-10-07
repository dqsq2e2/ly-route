/* ELF metadata fixture only; never loaded into a kernel. */
#define MODULE_INFO(name, value) \
	const char name[] __attribute__((section(".modinfo"), used)) = value

MODULE_INFO(module_name, "name=tag_dsa");
MODULE_INFO(module_license, "license=GPL");
#ifndef TEST_OMIT_DSA
MODULE_INFO(dsa_alias, "alias=dsa_tag:dsa");
#endif
#ifndef TEST_OMIT_EDSA
MODULE_INFO(edsa_alias, "alias=dsa_tag:edsa");
#endif
