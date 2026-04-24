message(STATUS "Building ux proxy client library")

add_subdirectory(proto)

add_library(ux_proxy STATIC
  clnt.cc
)

target_link_libraries(ux_proxy PUBLIC
  serr
  util_log
  rpc
  rpc_spchannel
  sigmap_proxy
  ux_proto
)

target_include_directories(ux_proxy PUBLIC ${CMAKE_SOURCE_DIR})
